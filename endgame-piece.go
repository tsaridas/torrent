package torrent

// EndgamePiece asks additional peers for still-missing chunks of piece pi at
// or after torrent byte offset off, when that piece is PiecePriorityNow.
// Unlike ShadowRequestAhead (a byte-offset window that may cross pieces), this
// stays on one critical piece — the cold-start "piece 0 / probe head" shape —
// and keeps whichever copy of each chunk lands first.
//
// off should be the file start (or read head) in torrent coordinates so a
// multi-file title that begins mid-piece does not duplicate the previous
// file's leading chunks. Negative off is treated as the piece start.
//
// When ClientConfig.EndgamePeerBudget is set, each peer may take duplicates
// only up to its own request-queue room (PeerMaxRequests minus outstanding
// requests and live shadows); maxChunks<=0 covers the whole remaining piece;
// maxShadowInFlight and perChunk are not applied. A live shadow older than
// shadowSilentAge does not block asking other peers.
//
// Otherwise (A/B off): up to perChunk peers for each of the first maxChunks
// missing chunks, bounded by maxShadowInFlight.
//
// Same wire accounting as ShadowRequestAhead (via shadowRequestOne):
// requestState untouched so the tracked holder keeps its request. A second
// copy is dropped as redundant. Candidates are unchoked peers that have the
// piece, fastest first. No-ops when the piece is not Now, is complete, or has
// no missing chunks. A not-Now skip increments endgamePieceNotNowSkipsVar.
func (t *Torrent) EndgamePiece(pi pieceIndex, off int64, maxChunks, perChunk int) int {
	peerBudget := t.cl != nil && t.cl.config.EndgamePeerBudget
	if pi < 0 {
		return 0
	}
	if !peerBudget && (maxChunks <= 0 || perChunk <= 0) {
		return 0
	}
	t.cl.lock()
	defer t.cl.unlock()
	if !t.haveInfo() || int(pi) >= t.numPieces() {
		return 0
	}
	if t.pieceComplete(pi) {
		return 0
	}
	p := t.piece(pi)
	if p.purePriority() != PiecePriorityNow {
		torrent.Add(endgamePieceNotNowSkipsVar, 1)
		return 0
	}
	nChunks := p.numChunks()
	if nChunks <= 0 {
		return 0
	}
	pieceLen := int64(t.info.PieceLength)
	chunkSize := int64(t.chunkSize)
	if pieceLen <= 0 || chunkSize <= 0 {
		return 0
	}
	pieceStart := int64(pi) * pieceLen
	startCI := chunkIndexType(0)
	if off > pieceStart {
		startCI = chunkIndexType((off - pieceStart) / chunkSize)
		if startCI >= nChunks {
			return 0
		}
	}
	var peers []shadowPeerCand
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		if !pc.peerHasPiece(pi) || (pc.peerChoking && !pc.peerAllowedFast.Contains(pi)) {
			continue
		}
		peers = append(peers, shadowPeerCand{pc, pc.downloadRate()})
	}
	if len(peers) == 0 {
		return 0
	}
	t.sortShadowPeers(peers)

	chunkLimit := maxChunks
	if peerBudget {
		if maxChunks <= 0 || maxChunks > int(nChunks) {
			chunkLimit = int(nChunks)
		}
	}

	inFlight := 0
	if !peerBudget {
		inFlight = t.shadowInFlightLocked()
	}
	sent, chunks := 0, 0
	firstChunkPeers := 0
	base := t.pieceRequestIndexOffset(pi)
	for ci := startCI; ci < nChunks && chunks < chunkLimit; ci++ {
		if !peerBudget && inFlight >= maxShadowInFlight {
			break
		}
		// dirtyChunks means received (same as ShadowRequestAhead): only ask
		// for chunks we still need.
		if p.chunkIndexDirty(ci) {
			continue
		}
		chunks++
		ri := base + RequestIndex(ci)
		holder := t.requestingPeer(ri)
		asked := 0
		peersForChunk := 0
		for _, c := range peers {
			if !peerBudget {
				if asked >= perChunk || inFlight >= maxShadowInFlight {
					break
				}
			}
			pc := c.pc
			if &pc.Peer == holder {
				continue
			}
			if peerBudget && pc.shadowPeerRoom() <= 0 {
				continue
			}
			if live, cancelled, silent := pc.shadowSlot(ri); live {
				if silent {
					// Free the already-asked slot so other peers can race.
					continue
				}
				asked++
				peersForChunk++
				continue
			} else if cancelled {
				// Free the perChunk slot; peer was told to stop.
				continue
			}
			if pc.validReceiveChunks[ri] > 0 {
				asked++
				peersForChunk++
				continue
			}
			if !pc.shadowRequestOne(ri) {
				continue
			}
			asked++
			peersForChunk++
			sent++
			if !peerBudget {
				inFlight++
			}
		}
		if chunks == 1 {
			firstChunkPeers = peersForChunk
		}
	}
	t.lastEndgameFirstChunkPeers = firstChunkPeers
	if sent > 0 {
		torrent.Add(endgamePieceRequestsVar, int64(sent))
	}
	return sent
}

// LastEndgameFirstChunkPeers is how many peers were asked (or already racing)
// for the first missing chunk of the most recent EndgamePiece call.
func (t *Torrent) LastEndgameFirstChunkPeers() int {
	t.cl.lock()
	defer t.cl.unlock()
	return t.lastEndgameFirstChunkPeers
}

// CountFreshNowPeers counts unchoked connections with peakRequests==0 that
// still have at least one incomplete Now piece.
func (t *Torrent) CountFreshNowPeers() int {
	t.cl.lock()
	defer t.cl.unlock()
	n := 0
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		if pc.peakRequests != 0 {
			continue
		}
		if pc.hasWantedNowPriorityPiece() {
			n++
		}
	}
	return n
}

const (
	// endgamePieceRequestsVar counts requests sent by EndgamePiece.
	endgamePieceRequestsVar = "endgame piece requests sent"
	// endgamePieceNotNowSkipsVar counts EndgamePiece calls that found the
	// piece no longer at PiecePriorityNow (demoted while a reader waited).
	endgamePieceNotNowSkipsVar = "endgame piece not-now skips"
)
