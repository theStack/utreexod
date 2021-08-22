// Copyright (c) 2021 The utreexo developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package wire

import (
	"bytes"
	"crypto/sha512"
	"fmt"
	"io"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

const (
	// MaxScriptSize is the maximum allowed length of a raw script.
	//
	// TODO: This is a duplicate of MaxScriptSize in package txscript.  However,
	// importing package txscript to wire will cause a import cycle so this is a
	// stopgap solution.
	MaxScriptSize = 10000
)

// LeafData is all the data that goes into a leaf in the utreexo accumulator.
// The data here serve two roles: commitments and data needed for verification.
//
// Commitment:   BlockHash is included in the LeafData to commit to a block.
// Verification: OutPoint is the OutPoint for the utxo being referenced.
//               Height, IsCoinbase, Amount, and PkScript is the data needed for
//               tx verification (script, signatures, etc).
type LeafData struct {
	BlockHash  *chainhash.Hash
	OutPoint   *OutPoint
	Height     int32
	IsCoinBase bool
	Amount     int64
	PkScript   []byte
}

// LeafHash concats and hashes all the data in LeafData.
func (l *LeafData) LeafHash() [32]byte {
	digest := sha512.New512_256()
	l.Serialize(digest)

	// TODO go 1.17 support slice to array conversion so we
	// can avoid this extra copy.
	hash := [32]byte{}
	copy(hash[:], digest.Sum(nil))
	return hash
}

// ToString turns a LeafData into a string for logging.
func (l *LeafData) ToString() (s string) {
	s += fmt.Sprintf("BlockHash:%x,", l.BlockHash)
	s += fmt.Sprintf("OutPoint:%s,", l.OutPoint.String())
	s += fmt.Sprintf("Amount:%d,", l.Amount)
	s += fmt.Sprintf("PkScript:%x,", l.PkScript)
	s += fmt.Sprintf("BlockHeight:%d,", l.Height)
	s += fmt.Sprintf("IsCoinBase:%v,", l.IsCoinBase)
	s += fmt.Sprintf("LeafHash:%x,", l.LeafHash())
	s += fmt.Sprintf("Size:%d", l.SerializeSize())
	return
}

// -----------------------------------------------------------------------------
// LeafData serialization includes all the data needed for generating the hash
// commitment of the LeafData.
//
// The serialized format is:
// [<block hash><outpoint><stxo>]
//
// The outpoint serialized format is:
// [<tx hash><index>]
//
// The stxo serialized format is:
// [<header code><amount><pkscript len><pkscript>]
//
// The serialized header code format is:
//   bit 0 - containing transaction is a coinbase
//   bits 1-x - height of the block that contains the spent txout
//
// It's calculated with:
//   header_code = <<= 1
//   if IsCoinBase {
//       header_code |= 1 // only set the bit 0 if it's a coinbase.
//   }
//
// All together, the serialization looks like so:
//
// Field              Type       Size
// block hash         [32]byte   32
// outpoint           -          33-36
//   tx hash          [32]byte   32
//   vout             VLQ        variable
// stxo               -          variable
//   header code      VLQ        variable
//   amount           VLQ        variable
//   pkscript length  VLQ        variable
//   pkscript         []byte     variable
//
// -----------------------------------------------------------------------------

// SerializeSize returns the number of bytes it would take to serialize the
// LeafData.
func (l *LeafData) SerializeSize() int {
	var size int
	size += VarIntSerializeSize(uint64(l.OutPoint.Index))
	size += VarIntSerializeSize(uint64(l.Height))
	size += VarIntSerializeSize(uint64(l.Amount))
	size += VarIntSerializeSize(uint64(len(l.PkScript)))

	// blockhash + txhash + pkscript size + others
	return chainhash.HashSize + chainhash.HashSize + len(l.PkScript) + size
}

// Serialize encodes the LeafData to w using the LeafData serialization format.
func (l *LeafData) Serialize(w io.Writer) error {
	hcb := l.Height << 1
	if l.IsCoinBase {
		hcb |= 1
	}

	// TODO Add the Blockhash back in.
	//_, err := w.Write(l.BlockHash[:])
	//if err != nil {
	//	return err
	//}
	var hash chainhash.Hash
	if bytes.Equal(l.OutPoint.Hash[:], hash[:]) {
		panic("l.OutPoint.Hash == hash")
	}
	_, err := w.Write(l.OutPoint.Hash[:])
	if err != nil {
		return err
	}
	err = WriteVarInt(w, 0, uint64(l.OutPoint.Index))
	if err != nil {
		return err
	}
	err = WriteVarInt(w, 0, uint64(hcb))
	if err != nil {
		return err
	}
	err = WriteVarInt(w, 0, uint64(l.Amount))
	if err != nil {
		return err
	}
	if uint32(len(l.PkScript)) > MaxScriptSize {
		return messageError("LeafData.Serialize", "pkScript too long")
	}

	return WriteVarBytes(w, 0, l.PkScript)
}

// Deserialize encodes the LeafData from r using the LeafData serialization format.
func (l *LeafData) Deserialize(r io.Reader) error {
	// TODO Deserialize the blockhash.
	//l.BlockHash = new(chainhash.Hash)
	//_, err := io.ReadFull(r, l.BlockHash[:])
	//if err != nil {
	//	return err
	//}

	// Deserialize the outpoint.
	l.OutPoint = &OutPoint{Hash: *(new(chainhash.Hash)), Index: 0}
	_, err := io.ReadFull(r, l.OutPoint.Hash[:])
	if err != nil {
		return err
	}

	index, err := ReadVarInt(r, 0)
	if err != nil {
		return err
	}
	l.OutPoint.Index = uint32(index)

	// Deserialize the stxo.
	height, err := ReadVarInt(r, 0)
	if err != nil {
		return err
	}
	l.Height = int32(height)

	if l.Height&1 == 1 {
		l.IsCoinBase = true
	}
	l.Height >>= 1

	amt, err := ReadVarInt(r, 0)
	if err != nil {
		return err
	}
	l.Amount = int64(amt)

	l.PkScript, err = ReadVarBytes(r, 0, MaxScriptSize, "pkscript size")
	if err != nil {
		return err
	}

	return nil
}

// -----------------------------------------------------------------------------
// Compact LeafData serialization leaves out duplicate data that is also present
// in the Bitcoin block.  It's important to note that to genereate the hash
// commitment for the LeafData, there data left out from the compact serialization
// is still needed and must be fetched from the Bitcoin block.
//
// The serialized format is:
// [<stxo>]
//
// The serialized header code format is:
//   bit 0 - containing transaction is a coinbase
//   bits 1-x - height of the block that contains the spent txout
//
// It's calculated with:
//   header_code = <<= 1
//   if IsCoinBase {
//       header_code |= 1 // only set the bit 0 if it's a coinbase.
//   }
//
// Field              Type       Size
// stxo               -          variable
//   header code      VLQ        variable
//   amount           VLQ        variable
//   pkscript length  VLQ        variable
//   pkscript         []byte     variable
//
// -----------------------------------------------------------------------------

// SerializeSizeCompact returns the number of bytes it would take to serialize the
// LeafData in the compact serialization format.
func (l *LeafData) SerializeSizeCompact() int {
	var size int
	hcb := l.Height << 1
	if l.IsCoinBase {
		hcb |= 1
	}
	size += VarIntSerializeSize(uint64(hcb))
	size += VarIntSerializeSize(uint64(l.Amount))
	size += VarIntSerializeSize(uint64(len(l.PkScript)))

	return size + len(l.PkScript)
}

// SerializeCompact encodes the LeafData to w using the compact leaf data serialization format.
func (l *LeafData) SerializeCompact(w io.Writer) error {
	hcb := l.Height << 1
	if l.IsCoinBase {
		hcb |= 1
	}

	// Height & IsCoinBase.
	err := WriteVarInt(w, 0, uint64(hcb))
	if err != nil {
		return err
	}

	err = WriteVarInt(w, 0, uint64(l.Amount))
	if err != nil {
		return err
	}
	if uint32(len(l.PkScript)) > MaxScriptSize {
		return messageError("LeafData.SerializeCompact", "pkScript too long")
	}

	return WriteVarBytes(w, 0, l.PkScript)
}

// DeserializeCompact encodes the LeafData to w using the compact leaf serialization format.
func (l *LeafData) DeserializeCompact(r io.Reader) error {
	height, err := ReadVarInt(r, 0)
	if err != nil {
		return err
	}
	l.Height = int32(height)

	if l.Height&1 == 1 {
		l.IsCoinBase = true
	}
	l.Height >>= 1

	amt, err := ReadVarInt(r, 0)
	if err != nil {
		return err
	}
	l.Amount = int64(amt)

	l.PkScript, err = ReadVarBytes(r, 0, MaxScriptSize, "pkScript size")
	if err != nil {
		return err
	}

	return nil
}

// NewLeafData initializes and returns a zeroed out LeafData.
func NewLeafData() LeafData {
	return LeafData{
		BlockHash: new(chainhash.Hash),
		OutPoint:  NewOutPoint(new(chainhash.Hash), 0),
	}
}
