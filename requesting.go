package torrent

import (
	"context"
	"encoding/gob"
	"fmt"
	"reflect"
	"runtime/pprof"
	"time"
	"unsafe"

	"github.com/RoaringBitmap/roaring"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/heap"
	"github.com/anacrolix/log"
	"github.com/anacrolix/multiless"

	requestStrategy "github.com/anacrolix/torrent/request-strategy"
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

type (
	// Since we have to store all the requests in memory, we can't reasonably exceed what could be
	// indexed with the memory space available.
	maxRequests = int
)

func (t *Torrent) requestStrategyPieceOrderState(i int) requestStrategy.PieceRequestOrderState {
	return requestStrategy.PieceRequestOrderState{
		Priority:     t.piece(i).purePriority(),
		Partial:      t.piecePartiallyDownloaded(i),
		Availability: t.piece(i).availability(),
	}
}

func init() {
	gob.Register(peerId{})
}

type peerId struct {
	*Peer
	ptr uintptr
}

func (p peerId) Uintptr() uintptr {
	return p.ptr
}

func (p peerId) GobEncode() (b []byte, _ error) {
	*(*reflect.SliceHeader)(unsafe.Pointer(&b)) = reflect.SliceHeader{
		Data: uintptr(unsafe.Pointer(&p.ptr)),
		Len:  int(unsafe.Sizeof(p.ptr)),
		Cap:  int(unsafe.Sizeof(p.ptr)),
	}
	return
}

func (p *peerId) GobDecode(b []byte) error {
	if uintptr(len(b)) != unsafe.Sizeof(p.ptr) {
		panic(len(b))
	}
	ptr := unsafe.Pointer(&b[0])
	p.ptr = *(*uintptr)(ptr)
	log.Printf("%p", ptr)
	dst := reflect.SliceHeader{
		Data: uintptr(unsafe.Pointer(&p.Peer)),
		Len:  int(unsafe.Sizeof(p.Peer)),
		Cap:  int(unsafe.Sizeof(p.Peer)),
	}
	copy(*(*[]byte)(unsafe.Pointer(&dst)), b)
	return nil
}

type (
	RequestIndex   = requestStrategy.RequestIndex
	chunkIndexType = requestStrategy.ChunkIndex
)

type desiredPeerRequests struct {
	requestIndexes []RequestIndex
	peer           *Peer
	pieceStates    []g.Option[requestStrategy.PieceRequestOrderState]
}

func (p *desiredPeerRequests) lessByValue(leftRequest, rightRequest RequestIndex) bool {
	t := p.peer.t
	leftPieceIndex := t.pieceIndexOfRequestIndex(leftRequest)
	rightPieceIndex := t.pieceIndexOfRequestIndex(rightRequest)
	ml := multiless.New()
	// Push requests that can't be served right now to the end. But we don't throw them away unless
	// there's a better alternative. This is for when we're using the fast extension and get choked
	// but our requests could still be good when we get unchoked.
	if p.peer.peerChoking {
		ml = ml.Bool(
			!p.peer.peerAllowedFast.Contains(leftPieceIndex),
			!p.peer.peerAllowedFast.Contains(rightPieceIndex),
		)
	}
	leftPiece := p.pieceStates[leftPieceIndex].UnwrapPtr()
	rightPiece := p.pieceStates[rightPieceIndex].UnwrapPtr()
	// Putting this first means we can steal requests from lesser-performing peers for our first few
	// new requests.
	priority := func() PiecePriority {
		// Technically we would be happy with the cached priority here, except we don't actually
		// cache it anymore, and Torrent.PiecePriority just does another lookup of *Piece to resolve
		// the priority through Piece.purePriority, which is probably slower.
		leftPriority := leftPiece.Priority
		rightPriority := rightPiece.Priority
		ml = ml.Int(
			-int(leftPriority),
			-int(rightPriority),
		)
		if !ml.Ok() {
			if leftPriority != rightPriority {
				panic("expected equal")
			}
		}
		return leftPriority
	}()
	if ml.Ok() {
		return ml.MustLess()
	}
	leftRequestState := t.requestState[leftRequest]
	rightRequestState := t.requestState[rightRequest]
	leftPeer := leftRequestState.peer
	rightPeer := rightRequestState.peer
	// Prefer chunks already requested from this peer.
	ml = ml.Bool(rightPeer == p.peer, leftPeer == p.peer)
	// Prefer unrequested chunks.
	ml = ml.Bool(rightPeer == nil, leftPeer == nil)
	if ml.Ok() {
		return ml.MustLess()
	}
	if leftPeer != nil {
		// The right peer should also be set, or we'd have resolved the computation by now.
		ml = ml.Uint64(
			rightPeer.requestState.Requests.GetCardinality(),
			leftPeer.requestState.Requests.GetCardinality(),
		)
		// Could either of the lastRequested be Zero? That's what checking an existing peer is for.
		leftLast := leftRequestState.when
		rightLast := rightRequestState.when
		if leftLast.IsZero() || rightLast.IsZero() {
			panic("expected non-zero last requested times")
		}
		// We want the most-recently requested on the left. Clients like Transmission serve requests
		// in received order, so the most recently-requested is the one that has the longest until
		// it will be served and therefore is the best candidate to cancel.
		ml = ml.CmpInt64(rightLast.Sub(leftLast).Nanoseconds())
	}
	ml = ml.Int(
		leftPiece.Availability,
		rightPiece.Availability)
	if priority == PiecePriorityReadahead {
		// TODO: For readahead in particular, it would be even better to consider distance from the
		// reader position so that reads earlier in a torrent don't starve reads later in the
		// torrent. This would probably require reconsideration of how readahead priority works.
		ml = ml.Int(leftPieceIndex, rightPieceIndex)
	} else {
		ml = ml.Int(t.pieceRequestOrder[leftPieceIndex], t.pieceRequestOrder[rightPieceIndex])
	}
	return ml.Less()
}

type desiredRequestState struct {
	Requests   desiredPeerRequests
	Interested bool
}

func (p *Peer) getDesiredRequestState() (desired desiredRequestState) {
	t := p.t
	if !t.haveInfo() {
		return
	}
	if t.closed.IsSet() {
		return
	}
	if t.dataDownloadDisallowed.Bool() {
		return
	}
	input := t.getRequestStrategyInput()
	requestHeap := desiredPeerRequests{
		peer:           p,
		pieceStates:    t.requestPieceStates,
		requestIndexes: t.requestIndexes,
	}
	clear(requestHeap.pieceStates)
	// Caller-provided allocation for roaring bitmap iteration.
	var it typedRoaring.Iterator[RequestIndex]
	requestStrategy.GetRequestablePieces(
		input,
		t.getPieceRequestOrder(),
		func(ih InfoHash, pieceIndex int, pieceExtra requestStrategy.PieceRequestOrderState) bool {
			if ih != *t.canonicalShortInfohash() {
				return false
			}
			if !p.peerHasPiece(pieceIndex) {
				return false
			}
			requestHeap.pieceStates[pieceIndex].Set(pieceExtra)
			allowedFast := p.peerAllowedFast.Contains(pieceIndex)
			t.iterUndirtiedRequestIndexesInPiece(&it, pieceIndex, func(r requestStrategy.RequestIndex) {
				if !allowedFast {
					// We must signal interest to request this. TODO: We could set interested if the
					// peers pieces (minus the allowed fast set) overlap with our missing pieces if
					// there are any readers, or any pending pieces.
					desired.Interested = true
					// We can make or will allow sustaining a request here if we're not choked, or
					// have made the request previously (presumably while unchoked), and haven't had
					// the peer respond yet (and the request was retained because we are using the
					// fast extension).
					if p.peerChoking && !p.requestState.Requests.Contains(r) {
						// We can't request this right now.
						return
					}
				}
				cancelled := &p.requestState.Cancelled
				if !cancelled.IsEmpty() && cancelled.Contains(r) {
					// Can't re-request while awaiting acknowledgement.
					return
				}
				requestHeap.requestIndexes = append(requestHeap.requestIndexes, r)
			})
			return true
		},
	)
	t.assertPendingRequests()
	desired.Requests = requestHeap
	return
}

func (p *Peer) maybeUpdateActualRequestState() {
	if p.closed.IsSet() {
		return
	}
	if p.needRequestUpdate == "" {
		return
	}
	if p.needRequestUpdate == peerUpdateRequestsTimerReason {
		since := time.Since(p.lastRequestUpdate)
		if since < updateRequestsTimerDuration {
			panic(since)
		}
	}
	pprof.Do(
		context.Background(),
		pprof.Labels("update request", string(p.needRequestUpdate)),
		func(_ context.Context) {
			next := p.getDesiredRequestState()
			p.applyRequestState(next)
			p.t.cacheNextRequestIndexesForReuse(next.Requests.requestIndexes)
		},
	)
}

func (t *Torrent) cacheNextRequestIndexesForReuse(slice []RequestIndex) {
	// The incoming slice can be smaller when getDesiredRequestState short circuits on some
	// conditions.
	if cap(slice) > cap(t.requestIndexes) {
		t.requestIndexes = slice[:0]
	}
}

// Whether we should allow sending not interested ("losing interest") to the peer. I noticed
// qBitTorrent seems to punish us for sending not interested when we're streaming and don't
// currently need anything.
func (p *Peer) allowSendNotInterested() bool {
	// Except for caching, we're not likely to lose pieces very soon.
	if p.t.haveAllPieces() {
		return true
	}
	all, known := p.peerHasAllPieces()
	if all || !known {
		return false
	}
	// Allow losing interest if we have all the pieces the peer has.
	return roaring.AndNot(p.peerPieces(), &p.t._completedPieces).IsEmpty()
}

// Transmit/action the request state to the peer.
func (p *Peer) applyRequestState(next desiredRequestState) {
	current := &p.requestState
	// Make interest sticky
	if !next.Interested && p.requestState.Interested {
		if !p.allowSendNotInterested() {
			next.Interested = true
		}
	}
	if !p.setInterested(next.Interested) {
		return
	}
	more := true
	orig := next.Requests.requestIndexes
	requestHeap := heap.InterfaceForSlice(
		&next.Requests.requestIndexes,
		next.Requests.lessByValue,
	)
	heap.Init(requestHeap)

	t := p.t
	originalRequestCount := current.Requests.GetCardinality()
	// A restricted peer may hold only a few now-priority requests; see
	// ClientConfig.NowPriorityRestrictedPeerRequests. nowHeld counts the ones
	// it already has so the cap spans update passes.
	nowLimit := t.cl.config.NowPriorityRestrictedPeerRequests
	slowRate := t.cl.config.NowPrioritySlowPeerRate
	myRate := p.downloadRate()
	restricted := nowLimit > 0 && peerRestrictedForNowPriority(myRate, slowRate)
	// The per-piece cap for a restricted peer, cached for this pass. It
	// mirrors torrent-stream's rank test: while a faster, proven peer holds
	// the piece, a proven slow peer takes none of it (rank returns false and
	// the wire goes elsewhere) and an untested peer gets its single proving
	// request. With no better holder, an untested peer gets nowLimit and a
	// proven slow peer is not restricted at all, so a sole slow seeder keeps
	// the download moving. See restrictedNowCap.
	pieceCap := map[pieceIndex]int{}
	nowCapFor := func(piece pieceIndex) int {
		if c, ok := pieceCap[piece]; ok {
			return c
		}
		better := false
		t.iterPeers(func(q *Peer) {
			if better || q == p || q.peerChoking || !q.peerHasPiece(piece) {
				return
			}
			better = fasterHolderAvailable(myRate, q.downloadRate(), slowRate)
		})
		c := restrictedNowCap(better, myRate, nowLimit)
		pieceCap[piece] = c
		return c
	}
	nowHeld := 0
	if restricted {
		current.Requests.Iterate(func(r RequestIndex) bool {
			if t.pieceIsNowPriority(r) {
				nowHeld++
			}
			return true
		})
	}
	for {
		if requestHeap.Len() == 0 {
			break
		}
		numPending := maxRequests(current.Requests.GetCardinality() + current.Cancelled.GetCardinality())
		if numPending >= p.nominalMaxRequests() {
			break
		}
		req := heap.Pop(requestHeap)
		if cap(next.Requests.requestIndexes) != cap(orig) {
			panic("changed")
		}

		// don't add requests on reciept of a reject - because this causes request back
		// to potentially permanently unresponive peers - which just adds network noise.  If
		// the peer can handle more requests it will send an "unchoked" message - which
		// will cause it to get added back to the request queue
		if p.needRequestUpdate == peerUpdateRequestsRemoteRejectReason {
			continue
		}

		existing := t.requestingPeer(req)
		// Skipping here sends the restricted peer on to the next, lower
		// priority request in the heap -- torrent-stream's "go elsewhere".
		countsTowardNow := false
		if restricted && existing != p && t.pieceIsNowPriority(req) {
			if c := nowCapFor(t.pieceIndexOfRequestIndex(req)); c >= 0 {
				if nowHeld >= c {
					torrent.Add(nowPriorityRestrictedSkipsVar, 1)
					continue
				}
				countsTowardNow = true
			}
		}
		if existing != nil && existing != p {
			// don't steal on cancel - because this is triggered by t.cancelRequest below
			// which means that the cancelled can immediately try to steal back a request
			// it has lost which can lead to circular cancel/add processing
			if p.needRequestUpdate == peerUpdateRequestsPeerCancelReason {
				continue
			}

			// Don't steal from the poor.
			diff := int64(current.Requests.GetCardinality()) + 1 - (int64(existing.uncancelledRequests()) - 1)
			// Steal a request that leaves us with one more request than the existing peer
			// connection if the stealer more recently received a chunk.
			allowSteal := diff <= 1 && (diff < 1 || p.lastUsefulChunkReceived.After(existing.lastUsefulChunkReceived))
			if !allowSteal && t.pieceIsNowPriority(req) {
				// Speed-based override, scoped to pieces needed for immediate
				// playback (PiecePriorityNow) only: the count-based check above
				// treats two peers with equal queue depth as equally good, but
				// says nothing about whether the existing holder is actually
				// delivering. A meaningfully faster (or stalled) alternative
				// peer should be allowed to take over a now-priority piece even
				// when queue depths are tied, matching the speed comparison
				// (otherSpeed < speed) the reference torrent-stream engine uses
				// for its stealing decisions.
				//
				// Use the non-locking downloadRate: applyRequestState already
				// runs under the Client lock, and Peer.DownloadRate (exported)
				// takes p.locker().RLock() itself, which deadlocks by
				// re-entering the same non-reentrant RWMutex on this goroutine.
				cfg := t.cl.config
				stealerRate, existingRate := p.downloadRate(), existing.downloadRate()
				allowSteal = stealAllowedBySpeed(
					stealerRate, existingRate,
					p.lastUsefulChunkReceived, existing.lastUsefulChunkReceived, time.Now(),
					cfg.NowPriorityStealSpeedFactor, cfg.NowPriorityStealStallThreshold,
				)
				if !allowSteal {
					// Neither rate-based test can see a request parked behind a
					// busy holder's queue; the deadline can. See
					// nowPriorityRequestOverdue.
					allowSteal = nowPriorityRequestOverdue(
						time.Since(t.requestState[req].when),
						cfg.NowPriorityRequestDeadline,
						p.lastUsefulChunkReceived, existing.lastUsefulChunkReceived, time.Now(),
						int64(current.Requests.GetCardinality()),
						int64(existing.uncancelledRequests()),
					)
				}
				if allowSteal {
					for _, f := range cfg.Callbacks.NowPriorityStealBySpeed {
						f(NowPriorityStealEvent{
							Torrent:      t,
							Piece:        t.pieceIndexOfRequestIndex(req),
							Stealer:      p,
							Existing:     existing,
							StealerRate:  stealerRate,
							ExistingRate: existingRate,
						})
					}
				}
			}
			if !allowSteal {
				continue
			}
			// Don't steal a request the current holder has not had time to answer. The tests
			// above compare queue depths, which say nothing about whether the block is already
			// on the wire; see ClientConfig.StealRequestGrace.
			if !t.stealRequestGraceElapsed(req) {
				continue
			}
			t.cancelRequest(req)
		}
		more = p.mustRequest(req)
		if countsTowardNow {
			nowHeld++
		}
		if !more {
			break
		}
	}
	if !more {
		// This might fail if we incorrectly determine that we can fit up to the maximum allowed
		// requests into the available write buffer space. We don't want that to happen because it
		// makes our peak requests dependent on how much was already in the buffer.
		panic(fmt.Sprintf(
			"couldn't fill apply entire request state [newRequests=%v]",
			current.Requests.GetCardinality()-originalRequestCount))
	}
	newPeakRequests := maxRequests(current.Requests.GetCardinality() - originalRequestCount)
	// log.Printf(
	// 	"requests %v->%v (peak %v->%v) reason %q (peer %v)",
	// 	originalRequestCount, current.Requests.GetCardinality(), p.peakRequests, newPeakRequests, p.needRequestUpdate, p)
	p.peakRequests = newPeakRequests
	p.needRequestUpdate = ""
	p.lastRequestUpdate = time.Now()
	if enableUpdateRequestsTimer {
		p.updateRequestsTimer.Reset(updateRequestsTimerDuration)
	}
}

// stealRequestGraceElapsed reports whether req has been outstanding with its current holder
// long enough for another peer to be allowed to take it. Always true when
// ClientConfig.StealRequestGrace is unset, which is the historical behaviour.
//
// The clock is requestState.when, which PeerConn.request rewrites on every issue, so the
// grace is measured against the current holder rather than the request's total lifetime.
func (t *Torrent) stealRequestGraceElapsed(req RequestIndex) bool {
	grace := t.cl.config.StealRequestGrace
	if grace <= 0 {
		return true
	}
	// The caller has already resolved a requesting peer for req, so the state is present.
	return time.Since(t.requestState[req].when) >= grace
}

// This could be set to 10s to match the unchoke/request update interval recommended by some
// specifications. I've set it shorter to trigger it more often for testing for now.
const (
	updateRequestsTimerDuration = 3 * time.Second
	enableUpdateRequestsTimer   = false
)

// pieceIsNowPriority reports whether req belongs to a piece the client needs
// immediately (e.g. for HLS playback at the current read position), as
// opposed to background/readahead pieces where the existing count-based
// stealing fairness should apply unchanged.
func (t *Torrent) pieceIsNowPriority(req RequestIndex) bool {
	return t.piece(t.pieceIndexOfRequestIndex(req)).purePriority() == PiecePriorityNow
}

// stealAllowedBySpeed decides the speed-based steal override for a
// now-priority piece: allow it when the existing holder has gone quiet for
// stall, or when the stealing peer's recent download rate exceeds the
// holder's by more than factor. A zero existingLast (holder has never
// delivered anything) counts as quiet, not as "just started," since a silent
// holder is exactly the case this override exists to unblock. factor <= 1
// disables the speed check; stall <= 0 disables the quiet-holder check.
//
// The quiet-holder branch additionally requires the stealer to have *earned*
// the block, via stealerBetter. Without that check the branch fires for every
// pair of peers at connection time -- neither has delivered a chunk yet, so
// both look quiet -- and a head piece is handed round a carousel of equally
// unproven peers, each holding it for one StealRequestGrace before the next
// takes it. Observed in production: one piece passed to five distinct peers
// inside a single second, every one of them at 0 B/s, with the peer that had
// just stolen it appearing as the victim of the next steal. The intent above
// is preserved -- a silent holder is still displaced -- but only by a peer
// that is actually delivering.
func stealAllowedBySpeed(
	stealerRate, existingRate float64,
	stealerLast, existingLast, now time.Time,
	factor float64,
	stall time.Duration,
) bool {
	stalled := stall > 0 &&
		(existingLast.IsZero() || now.Sub(existingLast) > stall) &&
		stealerBetter(stealerRate, existingRate, stealerLast, existingLast)
	fasterEnough := factor > 1 && stealerRate > 0 && stealerRate > existingRate*factor
	return stalled || fasterEnough
}

// stealerBetter reports whether the stealing peer has shown more recent or
// faster delivery than the holder. Either signal is enough: a peer with a
// higher measured rate, or one that delivered a useful chunk more recently.
// Two peers that have both delivered nothing satisfy neither, which is what
// breaks the cold-start carousel.
func stealerBetter(stealerRate, existingRate float64, stealerLast, existingLast time.Time) bool {
	if stealerRate > existingRate {
		return true
	}
	if stealerLast.IsZero() {
		return false
	}
	return existingLast.IsZero() || stealerLast.After(existingLast)
}

// nowPriorityRecentDelivery is how recently a stealer must have delivered a
// useful chunk to count as delivering right now for the deadline override.
const nowPriorityRecentDelivery = time.Second

// nowPriorityRequestOverdue decides the deadline steal override for a
// now-priority piece: allow it when the request has been outstanding with its
// current holder for longer than deadline, the stealing peer delivered a
// useful chunk within nowPriorityRecentDelivery, and either the holder has
// delivered nothing for a whole deadline or the stealer's queue is shallower
// than the holder's.
//
// The speed and stall overrides both read Peer.lastUsefulChunkReceived as a
// property of the *holder*. A holder that keeps delivering other pieces
// refreshes it continuously, so it never registers as stalled and -- if its
// overall rate is respectable -- never registers as slow either, while the one
// block that playback is waiting on sits behind its request queue. Measured on
// n200: a cold probe read took 30.8s to get 2 MiB, with a single steal at t=1s
// and nothing after, and the very next read on the same torrent pulled
// 7.86 MB in 993ms. The swarm was fine; one request was parked.
//
// age is measured from requestState.when, which PeerConn.request rewrites on
// every issue, so it is the age with the *current* holder rather than the
// request's total lifetime. A block therefore changes hands at most once per
// deadline, an order of magnitude longer than StealRequestGrace.
//
// The stealer gate is recency, not downloadRate: downloadRate is a lifetime
// average (BytesReadUsefulData / totalExpectingTime) and stays above zero
// forever once a peer has sent a single chunk, so a peer choked for a minute
// would still qualify and the block would move somewhere slower. Recency is
// also what keeps this from re-creating the cold-start carousel described on
// stealAllowedBySpeed -- at connection time no peer has delivered anything.
// The queue comparison stands in for the "don't steal from the poor" check the
// override bypasses: moving the block behind a deeper queue would not help.
func nowPriorityRequestOverdue(
	age, deadline time.Duration,
	stealerLast, holderLast, now time.Time,
	stealerQueue, holderQueue int64,
) bool {
	if deadline <= 0 || age <= deadline {
		return false
	}
	if stealerLast.IsZero() || now.Sub(stealerLast) > nowPriorityRecentDelivery {
		return false
	}
	// A holder that has delivered nothing for a whole deadline is not a
	// queue to compare against -- its depth says nothing about when the
	// block will arrive. This is also the common cold-start case: a peer
	// held to a couple of now-priority requests (NowPriorityRestrictedPeer
	// Requests) always has the shallower queue, so the comparison alone
	// would never let a proven peer take its block. Measured on n200: piece
	// 0 blocks sat with 0 B/s holders for ~3s while fast peers were
	// delivering other pieces.
	if holderLast.IsZero() || now.Sub(holderLast) > deadline {
		return true
	}
	return stealerQueue < holderQueue
}

// peerRestrictedForNowPriority reports whether a peer is limited to
// NowPriorityRestrictedPeerRequests on now-priority pieces. downloadRate is 0
// for a peer that has delivered nothing, so an untested peer is always
// restricted; a proven one is restricted while it runs below slowRate.
// slowRate <= 0 restricts only untested peers.
// restrictedNowCap is how many now-priority requests a restricted peer may
// hold on one piece; -1 means no cap. better reports whether a faster, proven
// peer holds the piece (fasterHolderAvailable). See nowCapFor in
// applyRequestState.
//
// An untested peer always keeps one request, even beside a better holder:
// torrent-stream gives every untested wire exactly one request, and
// delivering it is the only way the peer can prove itself. Capping it at zero
// strands a fast newcomer whenever the head piece is all there is to fetch --
// it never delivers, so it never becomes eligible to steal from a slow holder
// (TestBoostPieceStealsFromStalledReadaheadHolderInBudget in the server repo:
// 4.9s instead of well under the 700ms player budget).
func restrictedNowCap(better bool, myRate float64, nowLimit int) int {
	untested := myRate <= 0
	switch {
	case better && untested:
		return min(1, nowLimit)
	case better:
		return 0
	case untested:
		return nowLimit
	default:
		return -1
	}
}

// fasterHolderAvailable reports whether another peer holding a piece is a
// better source for it than a slow peer running at myRate: fast enough not to
// be restricted itself, and faster than myRate. This is torrent-stream's rank
// test (otherSpeed >= SPEED_THRESHOLD && otherSpeed > speed). Without it a
// sole slow seeder would be throttled to NowPriorityRestrictedPeerRequests
// with nobody to take up the slack.
func fasterHolderAvailable(myRate, otherRate, slowRate float64) bool {
	return otherRate >= slowRate && otherRate > myRate
}

// nowPriorityRestrictedSkipsVar is the expvar key (in the "torrent" map)
// counting now-priority requests a restricted peer was steered away from.
const nowPriorityRestrictedSkipsVar = "now-priority restricted skips"

func peerRestrictedForNowPriority(downloadRate, slowRate float64) bool {
	if downloadRate <= 0 {
		return true
	}
	return downloadRate < slowRate
}
