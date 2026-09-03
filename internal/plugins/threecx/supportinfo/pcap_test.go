package supportinfo

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// classic builds a little-endian libpcap file out of the frames given.
func classic(frames [][]byte) []byte {
	var out bytes.Buffer
	head := make([]byte, 24)
	binary.LittleEndian.PutUint32(head[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(head[4:6], 2)
	binary.LittleEndian.PutUint32(head[16:20], 262144)
	binary.LittleEndian.PutUint32(head[20:24], 1) // Ethernet
	out.Write(head)

	for i, frame := range frames {
		header := make([]byte, 16)
		binary.LittleEndian.PutUint32(header[0:4], 1_700_000_000)
		binary.LittleEndian.PutUint32(header[4:8], uint32(i*20_000))
		binary.LittleEndian.PutUint32(header[8:12], uint32(len(frame)))
		binary.LittleEndian.PutUint32(header[12:16], uint32(len(frame)))
		out.Write(header)
		out.Write(frame)
	}
	return out.Bytes()
}

// ng builds the same thing in pcapng, which is what a Windows PBX writes.
func ng(frames [][]byte) []byte {
	var out bytes.Buffer

	section := make([]byte, 28)
	binary.LittleEndian.PutUint32(section[0:4], 0x0a0d0d0a)
	binary.LittleEndian.PutUint32(section[4:8], 28)
	binary.LittleEndian.PutUint32(section[8:12], 0x1a2b3c4d)
	binary.LittleEndian.PutUint16(section[12:14], 1)
	binary.LittleEndian.PutUint64(section[16:24], ^uint64(0))
	binary.LittleEndian.PutUint32(section[24:28], 28)
	out.Write(section)

	iface := make([]byte, 20)
	binary.LittleEndian.PutUint32(iface[0:4], 1)
	binary.LittleEndian.PutUint32(iface[4:8], 20)
	binary.LittleEndian.PutUint16(iface[8:10], 1) // Ethernet
	binary.LittleEndian.PutUint32(iface[12:16], 262144)
	binary.LittleEndian.PutUint32(iface[16:20], 20)
	out.Write(iface)

	for i, frame := range frames {
		padded := (len(frame) + 3) &^ 3
		total := 32 + padded
		block := make([]byte, total)
		binary.LittleEndian.PutUint32(block[0:4], 6)
		binary.LittleEndian.PutUint32(block[4:8], uint32(total))
		stamp := uint64(1_700_000_000)*1e6 + uint64(i)*20_000
		binary.LittleEndian.PutUint32(block[12:16], uint32(stamp>>32))
		binary.LittleEndian.PutUint32(block[16:20], uint32(stamp))
		binary.LittleEndian.PutUint32(block[20:24], uint32(len(frame)))
		binary.LittleEndian.PutUint32(block[24:28], uint32(len(frame)))
		copy(block[28:], frame)
		binary.LittleEndian.PutUint32(block[total-4:], uint32(total))
		out.Write(block)
	}
	return out.Bytes()
}

// udp wraps a payload in Ethernet and IPv4, which is all the reader looks at.
func udp(src, dst [4]byte, srcPort, dstPort uint16, payload []byte) []byte {
	frame := make([]byte, 14+20+8+len(payload))
	binary.BigEndian.PutUint16(frame[12:14], 0x0800)

	ip := frame[14:]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+8+len(payload)))
	ip[9] = 17
	copy(ip[12:16], src[:])
	copy(ip[16:20], dst[:])

	datagram := ip[20:]
	binary.BigEndian.PutUint16(datagram[0:2], srcPort)
	binary.BigEndian.PutUint16(datagram[2:4], dstPort)
	binary.BigEndian.PutUint16(datagram[4:6], uint16(8+len(payload)))
	copy(datagram[8:], payload)
	return frame
}

// rtp builds one PCMU packet.
func rtp(sequence uint16, timestamp, ssrc uint32) []byte {
	p := make([]byte, 172)
	p[0] = 0x80 // version 2
	p[1] = 0    // PCMU
	binary.BigEndian.PutUint16(p[2:4], sequence)
	binary.BigEndian.PutUint32(p[4:8], timestamp)
	binary.BigEndian.PutUint32(p[8:12], ssrc)
	return p
}

var (
	phone = [4]byte{10, 10, 101, 25}
	carre = [4]byte{216, 82, 238, 16}
)

/*
Audio going out with nothing coming back is the whole reason for reading a
capture at all: it is dead air to whoever was on the phone, and it proves the
problem is between the two ends rather than on the PBX.
*/
func TestCaptureFindsOneWayAudio(t *testing.T) {
	var frames [][]byte
	for i := range 60 {
		frames = append(frames, udp(phone, carre, 9178, 34548, rtp(uint16(i), uint32(i*160), 0xAAAA)))
	}

	for _, file := range []struct {
		name string
		data []byte
	}{
		{"classic pcap", classic(frames)},
		{"pcapng", ng(frames)},
	} {
		t.Run(file.name, func(t *testing.T) {
			capture, err := readPcap(bytes.NewReader(file.data))
			if err != nil {
				t.Fatal(err)
			}
			if capture.Packets != 60 {
				t.Errorf("read %d packets, want 60", capture.Packets)
			}
			if len(capture.OneWay) != 1 {
				t.Fatalf("found %d one-way streams, want 1: %v", len(capture.OneWay), capture.OneWay)
			}
			if len(capture.Streams) != 1 || capture.Streams[0].Codec != "PCMU" {
				t.Errorf("streams read as %+v", capture.Streams)
			}
		})
	}
}

// Audio flowing both ways is the healthy case and must not be reported.
func TestCaptureAcceptsTwoWayAudio(t *testing.T) {
	var frames [][]byte
	for i := range 60 {
		frames = append(frames, udp(phone, carre, 9178, 34548, rtp(uint16(i), uint32(i*160), 0xAAAA)))
		frames = append(frames, udp(carre, phone, 34548, 9178, rtp(uint16(i), uint32(i*160), 0xBBBB)))
	}
	capture, err := readPcap(bytes.NewReader(classic(frames)))
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.OneWay) != 0 {
		t.Errorf("reported one-way audio on a healthy call: %v", capture.OneWay)
	}
	if len(capture.Streams) != 2 {
		t.Errorf("found %d streams, want 2", len(capture.Streams))
	}
}

// Gaps in the RTP sequence are packets that were sent and lost in transit,
// which is the difference between a network fault and a phone that never spoke.
func TestCaptureCountsLostPackets(t *testing.T) {
	var frames [][]byte
	for i := range 100 {
		if i%10 == 3 {
			continue // dropped on the way
		}
		frames = append(frames, udp(phone, carre, 9178, 34548, rtp(uint16(i), uint32(i*160), 0xAAAA)))
	}
	capture, err := readPcap(bytes.NewReader(classic(frames)))
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.Streams) != 1 {
		t.Fatalf("got %d streams, want 1", len(capture.Streams))
	}
	if got := capture.Streams[0].Lost; got != 10 {
		t.Errorf("counted %d lost packets, want 10", got)
	}

	findings := captureFindings(capture, "test")
	if len(findings) == 0 {
		t.Fatal("10% packet loss produced no finding")
	}
}

// SIP is how the call was set up, and a capture with no audio in it can still
// say whether the phone system ever agreed to one.
func TestCaptureCountsSIP(t *testing.T) {
	frames := [][]byte{
		udp(phone, carre, 5060, 5060, []byte("INVITE sip:100@example.com SIP/2.0\r\n")),
		udp(carre, phone, 5060, 5060, []byte("SIP/2.0 407 Proxy Authentication Required\r\n")),
		udp(phone, carre, 5060, 5060, []byte("BYE sip:100@example.com SIP/2.0\r\n")),
	}
	capture, err := readPcap(bytes.NewReader(classic(frames)))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, m := range capture.SIP {
		found[m.Name] = m.Count
	}
	if found["INVITE"] != 1 || found["BYE"] != 1 || found["response 407"] != 1 {
		t.Errorf("SIP read as %+v", capture.SIP)
	}
}

// A file we cannot read has to say so. Reporting no capture would lose the most
// useful thing in a Windows bundle without anybody noticing.
func TestCaptureRefusesWhatItCannotRead(t *testing.T) {
	if _, err := readPcap(bytes.NewReader([]byte("not a capture at all"))); err == nil {
		t.Error("read a file that is not a capture without complaining")
	}
}
