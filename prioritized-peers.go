package torrent

import (
	"hash/maphash"

	"github.com/anacrolix/multiless"
	"github.com/google/btree"
)

// Peers are stored with their priority at insertion. Their priority may
// change if our apparent IP changes, we don't currently handle that.
type prioritizedPeersItem struct {
	prio peerPriority
	p    PeerInfo
}

var hashSeed = maphash.MakeSeed()

func (me prioritizedPeersItem) addrHash() int64 {
	var h maphash.Hash
	h.SetSeed(hashSeed)
	h.WriteString(me.p.Addr.String())
	return int64(h.Sum64())
}

func (me prioritizedPeersItem) Less(than btree.Item) bool {
	other := than.(prioritizedPeersItem)
	return multiless.New().Bool(
		me.p.Trusted, other.p.Trusted).Int(
		peerSourceRank(me.p.Source), peerSourceRank(other.p.Source)).Uint32(
		me.prio, other.prio).Int64(
		me.addrHash(), other.addrHash(),
	).Less()
}

// peerSourceRank orders pending peers by where they came from, higher dialed
// first. Tracker and PEX peers were just reported live by a tracker or a
// connected peer; DHT results are older and more often dead. torrent-stream
// (stremio server.js) dials in discovery order, which in practice is tracker
// peers first, and reaches its first piece faster on cold starts for it.
func peerSourceRank(s PeerSource) int {
	switch s {
	case PeerSourceDirect, PeerSourceTracker, PeerSourcePex, PeerSourceIncoming:
		return 2
	case PeerSourceDhtGetPeers, PeerSourceDhtAnnouncePeer:
		return 0
	default:
		return 1
	}
}

func samePeerAddr(a, b PeerInfo) bool {
	return a.Addr.String() == b.Addr.String()
}

// betterPendingPeer reports whether a is preferred over b when both share an
// address: trusted wins, then source rank, then BEP 40 priority.
func betterPendingPeer(a, b prioritizedPeersItem) bool {
	if a.p.Trusted != b.p.Trusted {
		return a.p.Trusted
	}
	ra, rb := peerSourceRank(a.p.Source), peerSourceRank(b.p.Source)
	if ra != rb {
		return ra > rb
	}
	return a.prio > b.prio
}

type prioritizedPeers struct {
	om      *btree.BTree
	getPrio func(PeerInfo) peerPriority
}

func (me *prioritizedPeers) Each(f func(PeerInfo)) {
	me.om.Ascend(func(i btree.Item) bool {
		f(i.(prioritizedPeersItem).p)
		return true
	})
}

func (me *prioritizedPeers) Len() int {
	if me == nil || me.om == nil {
		return 0
	}
	return me.om.Len()
}

// Returns true if a peer is replaced.
func (me *prioritizedPeers) Add(p PeerInfo) bool {
	_, ok := me.AddReturningReplacedPeer(p)
	return ok
}

// AddReturningReplacedPeer inserts p, keeping one entry per address. Source
// is part of the btree sort key (tracker before DHT), so the same address
// from two sources would otherwise be two pending dials. We delete any
// same-address entries and keep the best (trusted, then source rank, then
// BEP 40 priority).
func (me *prioritizedPeers) AddReturningReplacedPeer(p PeerInfo) (ret PeerInfo, ok bool) {
	best := prioritizedPeersItem{me.getPrio(p), p}
	var same []prioritizedPeersItem
	me.om.Ascend(func(i btree.Item) bool {
		item := i.(prioritizedPeersItem)
		if samePeerAddr(item.p, p) {
			same = append(same, item)
		}
		return true
	})
	for _, item := range same {
		me.om.Delete(item)
		ok = true
		ret = item.p
		if betterPendingPeer(item, best) {
			best = item
		}
	}
	if prev := me.om.ReplaceOrInsert(best); prev != nil {
		ok = true
		ret = prev.(prioritizedPeersItem).p
	}
	return
}

func (me *prioritizedPeers) DeleteMin() (ret prioritizedPeersItem, ok bool) {
	i := me.om.DeleteMin()
	if i == nil {
		return
	}
	ret = i.(prioritizedPeersItem)
	ok = true
	return
}

func (me *prioritizedPeers) PopMax() PeerInfo {
	return me.om.DeleteMax().(prioritizedPeersItem).p
}
