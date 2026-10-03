package torrent

import "sort"

// maxShadowInFlight is a soft cap on outstanding shadow requests across all
// peers of one torrent. Stops a long wait from ramping duplicates toward
// peers×chunks as each 250ms round slides past already-shadowed peers.
const maxShadowInFlight = 256

// ShadowRequestAhead sends duplicate requests for the first maxChunks
// not-yet-received chunks at or after torrent offset off, each to up to
// perChunk additional peers, and returns how many requests it sent.
//
// This is libtorrent's time-critical shape, reduced to its core: when a
// reader is blocked on a block, ask more than one peer for it and keep
// whichever copy lands first. Our steal logic can only *move* a request, and
// only once someone has proved faster; at cold start nobody has, and a block
// assigned to a silent peer waits for the stall rule. Measured on n200 with
// block-level probe reads: the first block of the file sometimes arrived
// 5.7s after the read began, held by a 0 B/s peer, while other peers were
// delivering.
//
// A shadow request is sent on the wire and counted in validReceiveChunks, so
// the block is accepted when it arrives -- receiveChunk then treats it as
// unintended-but-useful, writes it, and cancels the tracked holder's request.
// A copy arriving second is dropped as redundant. requestState is untouched,
// so the tracked holder keeps its request until one of those happens.
// Peers that already have an outstanding (shadow) receive for a chunk count
// toward perChunk without sending again. Total outstanding shadows on the
// torrent are also bounded by maxShadowInFlight.
//
// Candidates are unchoked peers that have the piece, fastest first; the
// tracked holder is skipped.
func (t *Torrent) ShadowRequestAhead(off int64, maxChunks, perChunk int) int {
	if off < 0 || maxChunks <= 0 || perChunk <= 0 {
		return 0
	}
	t.cl.lock()
	defer t.cl.unlock()
	if !t.haveInfo() {
		return 0
	}
	pieceLen := int64(t.info.PieceLength)
	total := t.info.TotalLength()
	chunkSize := int64(t.chunkSize)
	if pieceLen <= 0 || chunkSize <= 0 {
		return 0
	}
	type cand struct {
		pc   *PeerConn
		rate float64
	}
	var conns []cand
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		conns = append(conns, cand{pc, pc.downloadRate()})
	}
	if len(conns) == 0 {
		return 0
	}
	if len(conns) > 4 {
		sort.Slice(conns, func(i, j int) bool { return conns[i].rate > conns[j].rate })
	}

	inFlight := t.shadowInFlightLocked()
	sent, chunks := 0, 0
	for pos := off; pos < total && chunks < maxChunks && inFlight < maxShadowInFlight; {
		pi := pieceIndex(pos / pieceLen)
		pieceStart := int64(pi) * pieceLen
		if t.pieceComplete(pi) {
			pos = pieceStart + int64(t.pieceLength(pi))
			continue
		}
		p := t.piece(pi)
		ci := chunkIndexType((pos - pieceStart) / chunkSize)
		pos = pieceStart + (int64(ci)+1)*chunkSize
		if p.chunkIndexDirty(ci) {
			continue
		}
		chunks++
		ri := t.pieceRequestIndexOffset(pi) + RequestIndex(ci)
		holder := t.requestingPeer(ri)
		asked := 0
		for _, c := range conns {
			if asked >= perChunk || inFlight >= maxShadowInFlight {
				break
			}
			pc := c.pc
			if &pc.Peer == holder || !pc.peerHasPiece(pi) || pc.peerChoking && !pc.peerAllowedFast.Contains(pi) {
				continue
			}
			if pc.validReceiveChunks[ri] > 0 {
				// Already outstanding — counts toward the cap so the next
				// round does not walk on to every other peer.
				asked++
				continue
			}
			if !pc.shadowRequestOne(ri) {
				continue
			}
			asked++
			sent++
			inFlight++
		}
	}
	if sent > 0 {
		torrent.Add(shadowRequestsSentVar, int64(sent))
	}
	return sent
}

func (t *Torrent) shadowInFlightLocked() int {
	n := 0
	for pc := range t.conns {
		n += len(pc.shadowRequests)
	}
	return n
}

// shadowRequestsSentVar is the expvar key (in the "torrent" map) counting
// requests sent by ShadowRequestAhead.
const shadowRequestsSentVar = "shadow requests sent"
