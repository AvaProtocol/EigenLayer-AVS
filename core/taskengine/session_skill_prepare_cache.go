package taskengine

import (
	"container/heap"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// skillPrepareTTL is how long a skill prepare can still be checked at submit.
// It matches the signing window: after that, submit already rejects the deadline.
const skillPrepareTTL = 30 * 24 * time.Hour

// Abandoned prepares each hold the addition and cloned workflows until submit
// or this TTL. Each prepare allocates a new policy id, so a caller that never
// submits would retain one entry per call. Caps bound that, per runner
// (owner, wallet, chain), per owner, and for the process. Evicting an entry
// is the same as a restart: submit falls back to the strict per-task check.
const (
	skillPrepareMaxPerRunner = 8
	skillPrepareMaxPerOwner  = 32
	skillPrepareMaxEntries   = 1024
)

// skillPrepareNode is one cache entry ordered by savedAt. index is maintained
// by the heap so a single entry can be removed without scanning the map.
type skillPrepareNode struct {
	id      string
	savedAt time.Time
	index   int
	snap    *skillPrepareSnapshot
}

// skillPrepareHeap is a min-heap of prepare entries. The expired ones are a
// prefix, so an insert drops that prefix instead of ranging every entry.
type skillPrepareHeap []*skillPrepareNode

func (h skillPrepareHeap) Len() int { return len(h) }

func (h skillPrepareHeap) Less(i, j int) bool {
	if h[i].savedAt.Equal(h[j].savedAt) {
		return h[i].id < h[j].id
	}
	return h[i].savedAt.Before(h[j].savedAt)
}

func (h skillPrepareHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *skillPrepareHeap) Push(x any) {
	node, ok := x.(*skillPrepareNode)
	if !ok || node == nil {
		panic("skillPrepareHeap: Push expected *skillPrepareNode")
	}
	node.index = len(*h)
	*h = append(*h, node)
}

func (h *skillPrepareHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	item.index = -1
	return item
}

// skillPrepareCache is the process-local prepare snapshots. byRunner and
// byOwner list policy ids oldest-insert first. Those slices stay short
// because of the caps, so removing one id is a linear scan of that slice.
type skillPrepareCache struct {
	byID     map[string]*skillPrepareNode
	bySaved  skillPrepareHeap
	byRunner map[string][]string
	byOwner  map[string][]string
}

func newSkillPrepareCache() *skillPrepareCache {
	return &skillPrepareCache{
		byID:     map[string]*skillPrepareNode{},
		byRunner: map[string][]string{},
		byOwner:  map[string][]string{},
	}
}

func skillPrepareRunnerKey(owner, wallet common.Address, chainID int64) string {
	return strconv.FormatInt(chainID, 10) + ":" + strings.ToLower(owner.Hex()) + ":" + strings.ToLower(wallet.Hex())
}

func skillPrepareOwnerKey(owner common.Address) string {
	return strings.ToLower(owner.Hex())
}

func (c *skillPrepareCache) remember(id string, snap skillPrepareSnapshot) {
	if c.byID[id] != nil {
		c.drop(id)
	}
	now := time.Now()
	c.dropExpired(now)
	runner := skillPrepareRunnerKey(snap.owner, snap.wallet, snap.chainID)
	owner := skillPrepareOwnerKey(snap.owner)
	c.trimKey(c.byRunner, runner, skillPrepareMaxPerRunner)
	c.trimKey(c.byOwner, owner, skillPrepareMaxPerOwner)
	c.trimGlobal(skillPrepareMaxEntries)

	snap.savedAt = now
	copied := snap
	node := &skillPrepareNode{id: id, savedAt: now, snap: &copied}
	heap.Push(&c.bySaved, node)
	c.byID[id] = node
	c.byRunner[runner] = append(c.byRunner[runner], id)
	c.byOwner[owner] = append(c.byOwner[owner], id)
}

func (c *skillPrepareCache) get(id string, now time.Time) *skillPrepareSnapshot {
	node := c.byID[id]
	if node == nil || node.snap == nil {
		return nil
	}
	if now.Sub(node.savedAt) > skillPrepareTTL {
		c.drop(id)
		return nil
	}
	return node.snap
}

// dropExpired removes the expired prefix of the savedAt heap. Later entries
// are still inside the signing window, so this does not walk them.
func (c *skillPrepareCache) dropExpired(now time.Time) {
	for c.bySaved.Len() > 0 {
		oldest := c.bySaved[0]
		if oldest == nil || now.Sub(oldest.savedAt) > skillPrepareTTL {
			if oldest == nil {
				heap.Pop(&c.bySaved)
				continue
			}
			c.drop(oldest.id)
			continue
		}
		return
	}
}

func (c *skillPrepareCache) trimKey(index map[string][]string, key string, limit int) {
	for len(index[key]) >= limit {
		oldest := c.oldestID(index[key])
		if oldest == "" {
			return
		}
		before := len(index[key])
		c.drop(oldest)
		if len(index[key]) >= before {
			return
		}
	}
}

func (c *skillPrepareCache) trimGlobal(limit int) {
	for len(c.byID) >= limit && c.bySaved.Len() > 0 {
		c.drop(c.bySaved[0].id)
	}
}

func (c *skillPrepareCache) oldestID(ids []string) string {
	oldest := ""
	var oldestAt time.Time
	for _, id := range ids {
		node := c.byID[id]
		if node == nil {
			continue
		}
		if oldest == "" || node.savedAt.Before(oldestAt) || (node.savedAt.Equal(oldestAt) && id < oldest) {
			oldest = id
			oldestAt = node.savedAt
		}
	}
	return oldest
}

func (c *skillPrepareCache) drop(id string) {
	node := c.byID[id]
	if node == nil {
		return
	}
	delete(c.byID, id)
	if node.index >= 0 && node.index < len(c.bySaved) && c.bySaved[node.index] == node {
		heap.Remove(&c.bySaved, node.index)
	}
	if node.snap == nil {
		return
	}
	c.unlink(c.byRunner, skillPrepareRunnerKey(node.snap.owner, node.snap.wallet, node.snap.chainID), id)
	c.unlink(c.byOwner, skillPrepareOwnerKey(node.snap.owner), id)
}

func (c *skillPrepareCache) unlink(index map[string][]string, key, id string) {
	ids := index[key]
	for i, cur := range ids {
		if cur != id {
			continue
		}
		ids = append(ids[:i], ids[i+1:]...)
		if len(ids) == 0 {
			delete(index, key)
			return
		}
		index[key] = ids
		return
	}
}

func (n *Engine) rememberSkillPrepare(policyID string, snap skillPrepareSnapshot) {
	if n == nil || policyID == "" {
		return
	}
	n.skillPrepareMu.Lock()
	defer n.skillPrepareMu.Unlock()
	if n.skillPrepare == nil {
		n.skillPrepare = newSkillPrepareCache()
	}
	n.skillPrepare.remember(strings.ToLower(policyID), snap)
}

func (n *Engine) skillPrepareFor(policyID string) *skillPrepareSnapshot {
	if n == nil || policyID == "" {
		return nil
	}
	n.skillPrepareMu.Lock()
	defer n.skillPrepareMu.Unlock()
	if n.skillPrepare == nil {
		return nil
	}
	return n.skillPrepare.get(strings.ToLower(policyID), time.Now())
}

func (n *Engine) forgetSkillPrepare(policyID string) {
	if n == nil || policyID == "" {
		return
	}
	n.skillPrepareMu.Lock()
	defer n.skillPrepareMu.Unlock()
	if n.skillPrepare == nil {
		return
	}
	n.skillPrepare.drop(strings.ToLower(policyID))
}
