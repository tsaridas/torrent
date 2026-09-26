package torrent

// ReadableUnverifiedLen reports how many contiguous bytes, starting at torrent
// offset off and up to max, are already on disk. A byte counts when its piece
// is complete (verified), or when its chunk has been received and every write
// for that piece has finished. The second case is NOT hash-verified.
//
// This exists for one caller: a container probe (ffprobe reading the header
// of a file being streamed). Ordinary reads wait for the whole piece to be
// received and hashed, and pieces on feature-length torrents are 2-16 MiB, so
// a probe that needs the first few blocks waits for the slowest block of the
// whole first piece. Measured on a real swarm: the probe was blocked on a
// partial first piece -- some blocks in, others parked with slow peers -- for
// 75% of its time. Reading received-but-unverified chunks lets the probe
// proceed as its bytes arrive.
//
// The cost is that the data may be wrong: a peer can send garbage that only
// fails the hash later. Callers must use this only where a bad read is
// recoverable (a probe result), never for bytes delivered as media.
//
// dirtyChunks is set before a chunk is written (receiveChunk unpends it first
// so it is not requested twice), so "dirty" alone does not mean on disk. A
// piece's pendingWrites counts writes in flight, so dirty AND pendingWrites ==
// 0 does: every dirty chunk of that piece has been written.
func (t *Torrent) ReadableUnverifiedLen(off, max int64) int64 {
	if off < 0 || max <= 0 {
		return 0
	}
	t.cl.rLock()
	defer t.cl.rUnlock()
	if !t.haveInfo() {
		return 0
	}
	pieceLen := int64(t.info.PieceLength)
	total := t.info.TotalLength()
	chunkSize := int64(t.chunkSize)
	if pieceLen <= 0 || chunkSize <= 0 {
		return 0
	}
	var n int64
	for n < max && off+n < total {
		pos := off + n
		pi := pieceIndex(pos / pieceLen)
		pieceStart := int64(pi) * pieceLen
		pieceEnd := pieceStart + int64(t.pieceLength(pi))
		if t.pieceComplete(pi) {
			n += pieceEnd - pos
			continue
		}
		p := t.piece(pi)
		p.pendingWritesMutex.Lock()
		writing := p.pendingWrites != 0
		p.pendingWritesMutex.Unlock()
		if writing {
			break
		}
		ci := chunkIndexType((pos - pieceStart) / chunkSize)
		if !p.chunkIndexDirty(ci) {
			break
		}
		chunkEnd := pieceStart + (int64(ci)+1)*chunkSize
		if chunkEnd > pieceEnd {
			chunkEnd = pieceEnd
		}
		n += chunkEnd - pos
	}
	if n > max {
		n = max
	}
	return n
}

// PieceHashFailures returns how many times piece i has failed its hash check.
// A caller that consumed unverified bytes (ReadableUnverifiedLen) snapshots
// this when it reads and compares later: an increase means the bytes it used
// were bad and whatever it derived from them must be discarded.
func (t *Torrent) PieceHashFailures(i int) int64 {
	t.cl.rLock()
	defer t.cl.rUnlock()
	if !t.haveInfo() || i < 0 || i >= t.numPieces() {
		return 0
	}
	return t.piece(i).hashFailures
}
