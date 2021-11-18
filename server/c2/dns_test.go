package c2

/*
	Sliver Implant Framework
	Copyright (C) 2021  Bishop Fox

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU General Public License for more details.

	You should have received a copy of the GNU General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

import (
	"bytes"
	"crypto/rand"
	"fmt"
	insecureRand "math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bishopfox/sliver/implant/sliver/transports/dnsclient"
	"github.com/bishopfox/sliver/protobuf/dnspb"
	"github.com/bishopfox/sliver/util/encoders"
	"google.golang.org/protobuf/proto"
)

var (
	example1  = "1.example.com."
	example2  = "something-longer.example.com."
	example3  = "something-even-longer.example.computer."
	c2Domains = []string{example1, example2, example3}

	timeout   = time.Second * 5
	retryWait = time.Second * 1
)

func randomDataRandomSize(maxSize int) []byte {
	buf := make([]byte, insecureRand.Intn(maxSize))
	rand.Read(buf)
	return buf
}

func randomData(size int) []byte {
	buf := make([]byte, size)
	rand.Read(buf)
	return buf
}

func shuffleDNSMsgs(a []*dnspb.DNSMessage) {
	for i := len(a) - 1; i > 0; i-- { // Fisher–Yates shuffle
		j := insecureRand.Intn(i + 1)
		a[i], a[j] = a[j], a[i]
	}
}

func randomDNSMsgs(t *testing.T, parent string, maxSize int, encoder encoders.Encoder, client *dnsclient.SliverDNSClient) ([]*dnspb.DNSMessage, []byte) {
	testData := randomDataRandomSize(maxSize)
	dnsMsgs := []*dnspb.DNSMessage{}
	msg := &dnspb.DNSMessage{
		Type: dnspb.DNSMessageType_DATA_FROM_IMPLANT,
		Size: uint32(len(testData)),
	}
	domains, err := client.SplitBuffer(msg, encoder, testData)
	if err != nil {
		t.Fatalf("failed to encode sample: %s", err)
	}
	for _, domain := range domains {
		subdata := strings.TrimSuffix(domain, parent)
		subdata = strings.ReplaceAll(subdata, ".", "")
		data, err := encoder.Decode([]byte(subdata))
		if err != nil {
			t.Fatalf("Unexpected error decoding subdata: %s", err)
		}
		msg := &dnspb.DNSMessage{}
		err = proto.Unmarshal(data, msg)
		if err != nil {
			t.Fatalf("Unexpected error un-marshaling subdata: %s", err)
		}
		dnsMsgs = append(dnsMsgs, msg)
	}
	shuffleDNSMsgs(dnsMsgs)
	return dnsMsgs, testData
}

func TestPendingEnvelopes(t *testing.T) {
	// *** Small ***
	for i := 0; i < 10; i++ {
		reassemble(t, example1, 256, encoders.Base58{})
		reassemble(t, example1, 256, encoders.Base32{})
	}
	// *** Large ***
	for i := 0; i < 10; i++ {
		reassemble(t, example1, 10*1024, encoders.Base58{})
		reassemble(t, example1, 10*1024, encoders.Base32{})
	}
}

func reassemble(t *testing.T, parent string, size int, encoder encoders.Encoder) {
	client := dnsclient.NewDNSClient(example1, timeout, retryWait)
	dnsMsgs, original := randomDNSMsgs(t, example1, size, encoder, client)
	dnsSession := DNSSession{
		ID:               dnsSessionID(),
		pendingEnvelopes: map[uint32]*PendingEnvelope{},
		mutex:            &sync.Mutex{},
	}

	// Re-assemble original message
	var pending *PendingEnvelope
	for _, dnsMsg := range dnsMsgs {
		pending = dnsSession.GetPendingEnvelope(dnsMsg.ID, dnsMsg.Size)
		if pending == nil {
			t.Fatal("GetPendingEnvelope returned nil")
		}
		complete := pending.Insert(dnsMsg)
		if complete {
			break
		}
	}
	data, err := pending.Reassemble()
	if err != nil {
		t.Logf("Original (%d): %v", len(original), original)
		for index, msg := range pending.messages.Values() {
			msgAssert := msg.(*dnspb.DNSMessage)
			t.Logf("%d | %v", index, msgAssert)
		}
		t.Fatalf("Failed to reassemble pending envelope: %s", err)
	}
	if !bytes.Equal(data, original) {
		t.Fatalf("Reassembled data does not match original\nOriginal: %v\nData: %v", original, data)
	}
}

func TestIsC2Domain(t *testing.T) {
	listener := StartDNSListener("", uint16(9999), c2Domains, false)
	isC2, domain := listener.isC2SubDomain(c2Domains, "asdf.1.example.com.")
	if !isC2 {
		t.Fatal("IsC2Domain expected true, got false")
	}
	if domain != example1 {
		t.Fatal("IsC2Domain expected example1, got", domain)
	}
	isC2, _ = listener.isC2SubDomain(c2Domains, "asdf.1.foobar.com.")
	if isC2 {
		t.Fatal("IsC2Domain expected false, got true (1)")
	}
	isC2, _ = listener.isC2SubDomain(c2Domains, "asdf.2.example.com.")
	if isC2 {
		t.Fatal("IsC2Domain expected false, got true (2)")
	}
	isC2, _ = listener.isC2SubDomain(c2Domains, fmt.Sprintf("asdf.asdf.asdf%s", example3))
	if isC2 {
		t.Fatal("IsC2Domain expected false, got true (3)")
	}
}

func TestDetermineLikelyEncoders(t *testing.T) {
	listener := StartDNSListener("", uint16(9999), c2Domains, false)
	sample := randomDataRandomSize(2048)
	b58Sample := string(encoders.Base58{}.Encode(sample))
	likelyEncoders := listener.determineLikelyEncoders(b58Sample)
	_, err := likelyEncoders[0].Decode([]byte(b58Sample))
	if err != nil {
		t.Error("DetermineLikelyEncoders failed to decode sample")
	}
}