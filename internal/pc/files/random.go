package files

import (
	"crypto/rand"
	"encoding/hex"
)

func randomID() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
