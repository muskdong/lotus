package chain

import (
	"sync"

	"github.com/filecoin-project/go-lotus/chain/address"
	"github.com/filecoin-project/go-lotus/chain/state"
	"github.com/filecoin-project/go-lotus/chain/types"
	hamt "github.com/ipfs/go-hamt-ipld"
)

type MessagePool struct {
	lk sync.Mutex

	pending map[address.Address]*msgSet

	cs *ChainStore
}

type msgSet struct {
	msgs       map[uint64]*types.SignedMessage
	startNonce uint64
}

func newMsgSet() *msgSet {
	return &msgSet{
		msgs: make(map[uint64]*types.SignedMessage),
	}
}

func (ms *msgSet) add(m *types.SignedMessage) {
	if len(ms.msgs) == 0 || m.Message.Nonce < ms.startNonce {
		ms.startNonce = m.Message.Nonce
	}
	ms.msgs[m.Message.Nonce] = m
}

func NewMessagePool(cs *ChainStore) *MessagePool {
	mp := &MessagePool{
		pending: make(map[address.Address]*msgSet),
		cs:      cs,
	}
	cs.SubscribeHeadChanges(mp.HeadChange)

	return mp
}

func (mp *MessagePool) Add(m *types.SignedMessage) error {
	mp.lk.Lock()
	defer mp.lk.Unlock()

	data, err := m.Message.Serialize()
	if err != nil {
		return err
	}

	if err := m.Signature.Verify(m.Message.From, data); err != nil {
		return err
	}

	msb, err := m.ToStorageBlock()
	if err != nil {
		return err
	}

	if err := mp.cs.bs.Put(msb); err != nil {
		return err
	}

	mset, ok := mp.pending[m.Message.From]
	if !ok {
		mset = newMsgSet()
		mp.pending[m.Message.From] = mset
	}

	mset.add(m)
	return nil
}

func (mp *MessagePool) GetNonce(addr address.Address) (uint64, error) {
	mp.lk.Lock()
	defer mp.lk.Unlock()

	mset, ok := mp.pending[addr]
	if ok {
		return mset.startNonce + uint64(len(mset.msgs)), nil
	}

	head := mp.cs.GetHeaviestTipSet()

	stc, err := mp.cs.TipSetState(head.Cids())
	if err != nil {
		return 0, err
	}

	cst := hamt.CSTFromBstore(mp.cs.bs)
	st, err := state.LoadStateTree(cst, stc)
	if err != nil {
		return 0, err
	}

	act, err := st.GetActor(addr)
	if err != nil {
		return 0, err
	}

	return act.Nonce, nil
}

func (mp *MessagePool) Remove(m *types.SignedMessage) {
	mp.lk.Lock()
	defer mp.lk.Unlock()

	mset, ok := mp.pending[m.Message.From]
	if !ok {
		return
	}

	// NB: This deletes any message with the given nonce. This makes sense
	// as two messages with the same sender cannot have the same nonce
	delete(mset.msgs, m.Message.Nonce)

	if len(mset.msgs) == 0 {
		delete(mp.pending, m.Message.From)
	}
}

func (mp *MessagePool) Pending() []*types.SignedMessage {
	mp.lk.Lock()
	defer mp.lk.Unlock()
	var out []*types.SignedMessage
	for _, mset := range mp.pending {
		for i := mset.startNonce; true; i++ {
			m, ok := mset.msgs[i]
			if !ok {
				break
			}
			out = append(out, m)
		}
	}

	return out
}

func (mp *MessagePool) HeadChange(revert []*TipSet, apply []*TipSet) error {
	for _, ts := range revert {
		for _, b := range ts.Blocks() {
			msgs, err := mp.cs.MessagesForBlock(b)
			if err != nil {
				return err
			}
			for _, msg := range msgs {
				if err := mp.Add(msg); err != nil {
					return err
				}
			}
		}
	}

	for _, ts := range apply {
		for _, b := range ts.Blocks() {
			msgs, err := mp.cs.MessagesForBlock(b)
			if err != nil {
				return err
			}
			for _, msg := range msgs {
				mp.Remove(msg)
			}
		}
	}

	return nil
}