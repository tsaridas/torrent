package torrent

// shadowRequestOne sends a duplicate request for ri on pc without touching
// requestState. The block is accepted when it arrives (validReceiveChunks);
// receiveChunk treats a second copy as redundant and cancels the tracked
// holder. Returns false if pc was already expecting that chunk.
func (pc *PeerConn) shadowRequestOne(ri RequestIndex) bool {
	if pc.validReceiveChunks[ri] > 0 {
		return false
	}
	if pc.validReceiveChunks == nil {
		pc.validReceiveChunks = make(map[RequestIndex]int)
	}
	if pc.shadowRequests == nil {
		pc.shadowRequests = make(map[RequestIndex]struct{})
	}
	pc.validReceiveChunks[ri]++
	pc.shadowRequests[ri] = struct{}{}
	pc._request(pc.t.requestIndexToRequest(ri))
	return true
}
