package supportinfo

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

/*
The packet capture, which is the only thing in the bundle that cannot argue.

Every other source is the phone system's account of what happened. This is what
actually went down the wire, and for the complaint that brings up a support
bundle more than any other — "we answered and there was no sound" — it is the
only evidence that settles it. Audio in a SIP call is RTP, RTP is one-way by
design, and a capture shows each direction separately. Either the packets came
back or they did not, and if they did not it was never a PBX problem.

Written against the format rather than with a library. libpcap's file layout is
a 24-byte header and then a length-prefixed packet each time, and the parts of
Ethernet, IPv4, UDP and RTP that matter here are all fixed-offset fields; the
whole reader is a couple of hundred lines of encoding/binary. Pulling in a
packet library to avoid them would add a large dependency, and its own parsers,
to a service that already handles hostile input for a living.

Nothing is held: the file streams past a bufio.Reader a packet at a time, so an
eighteen megabyte capture costs a few kilobytes of tallies rather than eighteen
megabytes of packet objects.
*/

// Capture is what the packet capture showed.
type Capture struct {
	Packets int       `json:"packets"`
	Seconds float64   `json:"seconds,omitempty"`
	From    time.Time `json:"from,omitempty"`
	To      time.Time `json:"to,omitempty"`
	// Truncated says the capture was longer than we were willing to read, so
	// the numbers describe the beginning of it rather than all of it.
	Truncated bool `json:"truncated,omitempty"`

	Protocols []Counted `json:"protocols,omitempty"`
	SIP       []Counted `json:"sip_methods,omitempty"`
	// Streams is the audio, one entry per direction.
	Streams []Stream `json:"streams,omitempty"`
	// OneWay names the conversations where audio went one way only.
	OneWay []string `json:"one_way,omitempty"`
}

// Stream is one direction of audio between two addresses.
type Stream struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	SSRC    string  `json:"ssrc"`
	Codec   string  `json:"codec,omitempty"`
	Packets int     `json:"packets"`
	Lost    int     `json:"lost,omitempty"`
	LossPct float64 `json:"loss_percent,omitempty"`
	Jitter  float64 `json:"jitter_ms,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
}

const (
	// maxPackets bounds the read. A capture is usually under a minute and
	// under two thousand packets; the cap is there so a deliberately enormous
	// one cannot spend the request.
	maxPackets = 400_000
	// jitterGain is RFC 3550's smoothing constant for interarrival jitter.
	jitterGain = 16.0
)

// payloadTypes are the RTP payload types 3CX uses, with their clock rates.
// The rate matters: jitter is computed in RTP timestamp units and has to be
// converted to milliseconds to mean anything.
var payloadTypes = map[byte]struct {
	name string
	rate float64
}{
	0:  {"PCMU", 8000},
	3:  {"GSM", 8000},
	4:  {"G723", 8000},
	8:  {"PCMA", 8000},
	9:  {"G722", 8000},
	18: {"G729", 8000},
}

// sipMethods are the request lines worth counting. A payload starting with one
// of these, or with the status line, is SIP.
var sipMethods = []string{
	"INVITE", "REGISTER", "BYE", "ACK", "CANCEL", "OPTIONS", "NOTIFY",
	"SUBSCRIBE", "INFO", "PRACK", "UPDATE", "REFER", "MESSAGE", "PUBLISH",
}

// rtpStream is one RTP flow while it is being counted.
type rtpStream struct {
	Stream
	payload byte
	// Sequence numbers bound how many packets should have arrived.
	minSeq, maxSeq uint16
	seen           int
	wrapped        bool
	// Jitter, per RFC 3550: the smoothed difference between how far apart two
	// packets were sent and how far apart they arrived.
	jitter     float64
	lastTS     uint32
	lastArrive float64
	started    bool
	first      float64
	last       float64
}

// tally is the running count of what a capture contained, shared by both file
// formats because only the container differs — the packets inside are the same.
type tally struct {
	packets   int
	protocols map[string]int
	sip       map[string]int
	streams   map[string]*rtpStream
	first     float64
	last      float64
}

func newTally() *tally {
	return &tally{
		protocols: map[string]int{}, sip: map[string]int{}, streams: map[string]*rtpStream{},
	}
}

func (t *tally) add(frame []byte, at float64) {
	t.packets++
	if t.first == 0 {
		t.first = at
	}
	t.last = at
	readFrame(frame, at, t.protocols, t.sip, t.streams)
}

func (t *tally) result() (*Capture, error) {
	if t.packets == 0 {
		return nil, fmt.Errorf("the packet capture was empty")
	}
	out := &Capture{
		Packets:   t.packets,
		From:      time.Unix(int64(t.first), 0).UTC(),
		To:        time.Unix(int64(t.last), 0).UTC(),
		Seconds:   round(t.last-t.first, 1),
		Protocols: counted(t.protocols, 6),
		SIP:       counted(t.sip, 8),
	}
	out.Streams, out.OneWay = finishStreams(t.streams)
	return out, nil
}

/*
readPcap reads a capture, in whichever of the two formats it arrived in.

Which one it is depends on the operating system underneath: a Linux PBX writes
classic libpcap and a Windows one writes pcapng. That is not a detail anybody
should have to know, and getting it wrong is silent — the reader that cannot
understand the file just reports no capture, and a Windows bundle loses the
single most useful thing in it without saying so.
*/
func readPcap(r io.Reader) (*Capture, error) {
	in := bufio.NewReaderSize(r, 1<<20)

	magic, err := in.Peek(4)
	if err != nil {
		return nil, fmt.Errorf("not a packet capture: %w", err)
	}
	if binary.BigEndian.Uint32(magic) == 0x0a0d0d0a {
		return readPcapng(in)
	}
	return readClassicPcap(in)
}

// readClassicPcap reads the original libpcap format: a 24-byte file header and
// then a length-prefixed packet each time.
func readClassicPcap(in *bufio.Reader) (*Capture, error) {
	var head [24]byte
	if _, err := io.ReadFull(in, head[:]); err != nil {
		return nil, fmt.Errorf("not a packet capture: %w", err)
	}

	var order binary.ByteOrder
	nanos := false
	switch binary.BigEndian.Uint32(head[:4]) {
	case 0xa1b2c3d4:
		order = binary.BigEndian
	case 0xd4c3b2a1:
		order = binary.LittleEndian
	case 0xa1b23c4d:
		order, nanos = binary.BigEndian, true
	case 0x4d3cb2a1:
		order, nanos = binary.LittleEndian, true
	default:
		return nil, fmt.Errorf("not a packet capture")
	}

	// Only Ethernet is handled. A capture off a tunnel interface would need
	// its own link layer, and guessing at offsets produces confident nonsense.
	if linkType := order.Uint32(head[20:24]); linkType != 1 {
		return nil, fmt.Errorf("packet capture uses link type %d, which this reader does not understand", linkType)
	}

	counts := newTally()
	var packet [1 << 16]byte
	var header [16]byte

	for counts.packets < maxPackets {
		if _, err := io.ReadFull(in, header[:]); err != nil {
			break
		}
		fraction := float64(order.Uint32(header[4:8]))
		if nanos {
			fraction /= 1e9
		} else {
			fraction /= 1e6
		}
		at := float64(order.Uint32(header[0:4])) + fraction

		captured := int(order.Uint32(header[8:12]))
		if captured < 0 || captured > len(packet) {
			break
		}
		if _, err := io.ReadFull(in, packet[:captured]); err != nil {
			break
		}
		counts.add(packet[:captured], at)
	}

	out, err := counts.result()
	if err != nil {
		return nil, err
	}
	out.Truncated = truncated(in, counts.packets)
	return out, nil
}

/*
readPcapng reads the next-generation format, which is what Windows writes.

Everything is a block: a type, a length, a body, and the length again so the
file can be read backwards. Only two block types matter here — the interface
description, which says what link layer the packets are in and how finely the
timestamps are counted, and the enhanced packet block, which is a packet. The
rest are skipped by their own length, which is what the format is for.
*/
func readPcapng(in *bufio.Reader) (*Capture, error) {
	var header [8]byte
	if _, err := io.ReadFull(in, header[:]); err != nil {
		return nil, fmt.Errorf("not a packet capture: %w", err)
	}

	// The section header carries a byte-order mark rather than encoding the
	// order in its type, so the first block has to be read twice in effect:
	// once to find the mark and once knowing what it means.
	var order binary.ByteOrder = binary.LittleEndian
	length := order.Uint32(header[4:8])
	if length < 12 || length > 1<<20 {
		order = binary.BigEndian
		length = order.Uint32(header[4:8])
		if length < 12 || length > 1<<20 {
			return nil, fmt.Errorf("not a packet capture")
		}
	}
	body := make([]byte, length-8)
	if _, err := io.ReadFull(in, body); err != nil {
		return nil, fmt.Errorf("not a packet capture: %w", err)
	}
	if binary.BigEndian.Uint32(body[:4]) == 0x1a2b3c4d {
		order = binary.BigEndian
	}

	counts := newTally()
	// Timestamps are counted in units the interface declares. A sixth of a
	// millionth is the default and by far the most common.
	resolution := 1e6
	linkType := uint16(1)
	interfaceSeen := false

	for counts.packets < maxPackets {
		if _, err := io.ReadFull(in, header[:]); err != nil {
			break
		}
		kind := order.Uint32(header[0:4])
		total := order.Uint32(header[4:8])
		if total < 12 || total > 1<<24 {
			break
		}
		body := make([]byte, total-8)
		if _, err := io.ReadFull(in, body); err != nil {
			break
		}
		// The trailing length is the last four bytes of the body as read.
		payload := body[:len(body)-4]

		switch kind {
		case 0x00000001: // interface description
			if len(payload) < 8 {
				break
			}
			if !interfaceSeen {
				linkType = order.Uint16(payload[0:2])
				interfaceSeen = true
			}
			if unit, ok := timestampUnit(payload[8:], order); ok {
				resolution = unit
			}

		case 0x00000006: // enhanced packet
			if len(payload) < 20 || linkType != 1 {
				break
			}
			high := uint64(order.Uint32(payload[4:8]))
			low := uint64(order.Uint32(payload[8:12]))
			at := float64(high<<32|low) / resolution
			captured := int(order.Uint32(payload[12:16]))
			if captured < 0 || 20+captured > len(payload) {
				break
			}
			counts.add(payload[20:20+captured], at)
		}
	}

	if !interfaceSeen || linkType != 1 {
		return nil, fmt.Errorf("packet capture uses link type %d, which this reader does not understand", linkType)
	}

	out, err := counts.result()
	if err != nil {
		return nil, err
	}
	out.Truncated = truncated(in, counts.packets)
	return out, nil
}

/*
timestampUnit reads if_tsresol out of an interface's options.

Options are a type, a length, and a padded value. Option 9 is one byte: below
128 it is a power of ten, above it a power of two. Almost every file in
existence says six, meaning microseconds, but a capture written in nanoseconds
would otherwise land its packets a thousand years apart.
*/
func timestampUnit(options []byte, order binary.ByteOrder) (float64, bool) {
	for len(options) >= 4 {
		code := order.Uint16(options[0:2])
		length := int(order.Uint16(options[2:4]))
		if code == 0 { // end of options
			return 0, false
		}
		padded := (length + 3) &^ 3
		if 4+padded > len(options) {
			return 0, false
		}
		if code == 9 && length == 1 {
			exponent := options[4]
			if exponent&0x80 != 0 {
				return math.Pow(2, float64(exponent&0x7f)), true
			}
			return math.Pow(10, float64(exponent)), true
		}
		options = options[4+padded:]
	}
	return 0, false
}

// truncated reports whether the cap stopped the read with file left over.
func truncated(in *bufio.Reader, packets int) bool {
	if packets < maxPackets {
		return false
	}
	_, err := in.ReadByte()
	return err == nil
}

// readFrame picks one packet apart as far as it is understood.
func readFrame(frame []byte, at float64, protocols, sip map[string]int, streams map[string]*rtpStream) {
	const ethernet = 14
	if len(frame) < ethernet+20 {
		return
	}
	if binary.BigEndian.Uint16(frame[12:14]) != 0x0800 {
		protocols["other"]++
		return
	}

	ip := frame[ethernet:]
	length := int(ip[0]&0x0f) * 4
	if length < 20 || len(ip) < length {
		return
	}
	src, dst := ipString(ip[12:16]), ipString(ip[16:20])

	switch ip[9] {
	case 1:
		protocols["icmp"]++
	case 6:
		protocols["tcp"]++
	case 17:
		protocols["udp"]++
		readUDP(ip[length:], src, dst, at, sip, streams)
	default:
		protocols["other"]++
	}
}

func readUDP(datagram []byte, src, dst string, at float64, sip map[string]int, streams map[string]*rtpStream) {
	const udpHeader = 8
	if len(datagram) < udpHeader {
		return
	}
	srcPort := binary.BigEndian.Uint16(datagram[0:2])
	dstPort := binary.BigEndian.Uint16(datagram[2:4])
	body := datagram[udpHeader:]
	if len(body) == 0 {
		return
	}

	if method, ok := sipLine(body); ok {
		sip[method]++
		return
	}

	// RTP: version 2 in the top two bits, and a payload type we recognise.
	// Both checks matter — plenty of traffic has 0b10 in the right place, and
	// a lone version bit would classify half the internet as audio.
	if len(body) < 12 || body[0]>>6 != 2 {
		return
	}
	payload := body[1] & 0x7f
	kind, known := payloadTypes[payload]
	if !known {
		return
	}

	ssrc := binary.BigEndian.Uint32(body[8:12])
	sequence := binary.BigEndian.Uint16(body[2:4])
	timestamp := binary.BigEndian.Uint32(body[4:8])

	from := fmt.Sprintf("%s:%d", src, srcPort)
	to := fmt.Sprintf("%s:%d", dst, dstPort)
	key := fmt.Sprintf("%s>%s/%08x", from, to, ssrc)

	s, ok := streams[key]
	if !ok {
		s = &rtpStream{
			Stream:  Stream{From: from, To: to, SSRC: fmt.Sprintf("%08x", ssrc), Codec: kind.name},
			payload: payload,
			minSeq:  sequence, maxSeq: sequence,
			first: at,
		}
		streams[key] = s
	}

	s.seen++
	s.last = at
	if sequence < s.minSeq {
		s.minSeq = sequence
	}
	if sequence > s.maxSeq {
		s.maxSeq = sequence
	}
	// A capture long enough to wrap the 16-bit sequence would make the loss
	// arithmetic below meaningless, so it is abandoned rather than guessed at.
	if s.seen > 1 && int(s.maxSeq)-int(s.minSeq)+1 < s.seen {
		s.wrapped = true
	}

	if s.started {
		// RFC 3550: how much further apart these two packets arrived than they
		// were sent, smoothed. Positive either way, hence the absolute value.
		sent := (float64(timestamp) - float64(s.lastTS)) / kind.rate
		arrived := at - s.lastArrive
		drift := math.Abs(arrived - sent)
		s.jitter += (drift - s.jitter) / jitterGain
	}
	s.started = true
	s.lastTS = timestamp
	s.lastArrive = at
}

// sipLine reports whether a datagram is SIP, and what it was.
func sipLine(body []byte) (string, bool) {
	if len(body) < 8 {
		return "", false
	}
	head := string(body[:min(len(body), 16)])
	if strings.HasPrefix(head, "SIP/2.0 ") {
		// A response. The code is the interesting part, not the reason phrase.
		if len(body) >= 12 {
			return "response " + string(body[8:11]), true
		}
		return "response", true
	}
	for _, method := range sipMethods {
		if strings.HasPrefix(head, method+" ") {
			return method, true
		}
	}
	return "", false
}

// finishStreams turns the tallies into results, and works out which
// conversations only ever went one way.
func finishStreams(streams map[string]*rtpStream) ([]Stream, []string) {
	out := make([]Stream, 0, len(streams))
	// Keyed by direction so the reverse of each can be looked for.
	directions := map[string]bool{}

	for _, s := range streams {
		if s.seen == 0 {
			continue
		}
		done := s.Stream
		done.Packets = s.seen
		done.Seconds = round(s.last-s.first, 1)
		done.Jitter = round(s.jitter*1000, 1)

		if !s.wrapped {
			expected := int(s.maxSeq) - int(s.minSeq) + 1
			if lost := expected - s.seen; lost > 0 {
				done.Lost = lost
				done.LossPct = round(float64(lost)*100/float64(expected), 1)
			}
		}
		out = append(out, done)
		directions[done.From+">"+done.To] = true
	}

	var oneWay []string
	for _, s := range out {
		// Silence is only worth reporting where there was something to answer:
		// a handful of packets is a stream that was still setting up.
		if s.Packets < 20 {
			continue
		}
		if !directions[s.To+">"+s.From] {
			oneWay = append(oneWay, fmt.Sprintf("%s → %s", s.From, s.To))
		}
	}
	sort.Strings(oneWay)

	sort.SliceStable(out, func(i, j int) bool { return out[i].Packets > out[j].Packets })
	if len(out) > 24 {
		out = out[:24]
	}
	return out, oneWay
}

func ipString(b []byte) string {
	return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
}

// captureFindings reads the capture for the things worth saying out loud.
func captureFindings(c *Capture, source string) []Finding {
	if c == nil {
		return nil
	}
	var out []Finding

	if len(c.OneWay) > 0 {
		out = append(out, Finding{
			Severity: Critical,
			Title:    fmt.Sprintf("Audio went one way only on %s", plural(len(c.OneWay), "stream", "streams")),
			Detail: "The capture has audio leaving for these addresses and nothing coming back from them. " +
				"That is dead air for whoever was on the phone, and because the packets are missing on the " +
				"wire rather than inside the phone system it is a firewall, NAT or routing problem between " +
				"the two — not something to fix on the PBX.",
			Evidence:    c.OneWay[:min(len(c.OneWay), maxEvidence)],
			Occurrences: len(c.OneWay),
			Source:      source,
		})
	}

	// Loss is judged on the streams big enough for a percentage to mean
	// something. 1% is audible on a voice call; 5% is unusable.
	var lossy []string
	worst := 0.0
	for _, s := range c.Streams {
		if s.Packets < 50 || s.LossPct < 1 {
			continue
		}
		lossy = append(lossy, fmt.Sprintf("%s → %s lost %.1f%% (%d of %d)",
			s.From, s.To, s.LossPct, s.Lost, s.Lost+s.Packets))
		worst = math.Max(worst, s.LossPct)
	}
	if len(lossy) > 0 {
		severity := Warning
		if worst >= 5 {
			severity = Critical
		}
		out = append(out, Finding{
			Severity: severity,
			Title:    fmt.Sprintf("Packet loss on %s in the capture", plural(len(lossy), "audio stream", "audio streams")),
			Detail: fmt.Sprintf("Worst was %.1f%%. Around 1%% is audible as clipping on a voice call and 5%% "+
				"makes one hard to hold. The gaps are in the RTP sequence numbers, so the packets were sent "+
				"and lost in transit rather than never sent.", worst),
			Evidence:    lossy[:min(len(lossy), maxEvidence)],
			Occurrences: len(lossy),
			Source:      source,
		})
	}
	return out
}
