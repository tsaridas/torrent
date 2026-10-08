package torrent

import "sort"

// shadowPeerCand is a peer ranked for ShadowRequestAhead / EndgamePiece.
type shadowPeerCand struct {
	pc   *PeerConn
	rate float64
}

func shadowPeerTier(pc *PeerConn) (delivered, seeder bool) {
	delivered = !pc.lastUsefulChunkReceived.IsZero()
	all, known := pc.peerHasAllPieces()
	seeder = known && all
	return
}

// sortShadowPeers sorts candidates by downloadRate desc. When
// ClientConfig.ShadowPeerOrder is set and every rate is 0, break ties by
// whether the peer has delivered anything, then whether it has all pieces,
// then rotate within equal tiers so successive ticks do not always prefer
// the same map-first peer.
func (t *Torrent) sortShadowPeers(peers []shadowPeerCand) {
	if len(peers) <= 1 {
		return
	}
	allZero := true
	for _, c := range peers {
		if c.rate != 0 {
			allZero = false
			break
		}
	}
	if !allZero || t.cl == nil || !t.cl.config.ShadowPeerOrder {
		sort.Slice(peers, func(i, j int) bool { return peers[i].rate > peers[j].rate })
		return
	}
	sort.SliceStable(peers, func(i, j int) bool {
		aDel, aSeeder := shadowPeerTier(peers[i].pc)
		bDel, bSeeder := shadowPeerTier(peers[j].pc)
		if aDel != bDel {
			return aDel
		}
		if aSeeder != bSeeder {
			return aSeeder
		}
		return false
	})
	rot := t.shadowPeerRotate
	t.shadowPeerRotate++
	for i := 0; i < len(peers); {
		j := i + 1
		ai, aj := shadowPeerTier(peers[i].pc)
		for j < len(peers) {
			bi, bj := shadowPeerTier(peers[j].pc)
			if ai != bi || aj != bj {
				break
			}
			j++
		}
		run := peers[i:j]
		if n := len(run); n > 1 {
			r := int(rot % uint64(n))
			if r > 0 {
				tmp := make([]shadowPeerCand, n)
				copy(tmp, run)
				for k := 0; k < n; k++ {
					run[k] = tmp[(k+r)%n]
				}
			}
		}
		i = j
	}
}
