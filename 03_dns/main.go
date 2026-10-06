package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const (
	dnsHeaderSize          = 12
	dnsFlagsOffset         = 2
	dnsQuestionCountOffset = 4
	dnsAnswerCountOffset   = 6
	dnsUint16Size          = 2
	dnsRCodeMask           = 0x0F
	dnsQuestionFieldsSize  = 4
	dnsRecordHeaderSize    = 10
)

type dnsAnswer struct {
	recordType string
	value      string
	ttl        uint32
}

type dnsResponse struct {
	rcode       uint16
	answers     []dnsAnswer
	minTTL      uint32
	answerCount uint16
}

type cacheKey struct {
	name       string
	recordType uint16
}

type cacheEntry struct {
	response  dnsResponse
	expiresAt time.Time
}

const (
	dnsTypeA     = 1
	dnsTypeNS    = 2
	dnsTypeCNAME = 5
	dnsTypeMX    = 15
	dnsTypeTXT   = 16
	dnsTypeAAAA  = 28
)

var recordTypes = map[string]uint16{
	"A":     dnsTypeA,
	"NS":    dnsTypeNS,
	"CNAME": dnsTypeCNAME,
	"MX":    dnsTypeMX,
	"TXT":   dnsTypeTXT,
	"AAAA":  dnsTypeAAAA,
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: dns <server address> <port>")
	}

	addr := net.JoinHostPort(args[0], args[1])
	if net.ParseIP(args[0]) == nil {
		return errors.New("server address must be an IP address")
	}
	cache := make(map[cacheKey]cacheEntry)
	var id uint16
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) != 2 {
			return errors.New("expected: <name> <type>")
		}

		name := parts[0]
		queryType, ok := recordTypes[strings.ToUpper(parts[1])]
		if !ok {
			return fmt.Errorf("unsupported query type: %s", parts[1])
		}

		fmt.Printf("query %s %s\n", name, parts[1])
		key := cacheKey{
			name:       name,
			recordType: queryType,
		}
		if entry, ok := cache[key]; ok {
			if time.Now().Before(entry.expiresAt) {
				printResponse(entry.response)
				continue
			}
			delete(cache, key)
		}

		id++
		packet := buildQuery(name, queryType, id)
		responseBytes, err := sendPacket(addr, packet, id)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				fmt.Print("status TIMEOUT\nend\n")
			}
			return err
		}
		response, err := parseResponse(responseBytes)
		if err != nil {
			return err
		}
		if response.rcode == 0 && response.answerCount > 0 && response.minTTL > 0 {
			cache[key] = cacheEntry{
				response:  response,
				expiresAt: time.Now().Add(time.Duration(response.minTTL) * time.Second),
			}
		}
		printResponse(response)
	}
	return scanner.Err()
}

func buildQuery(name string, queryType, id uint16) []byte {
	var header [dnsHeaderSize]byte

	binary.BigEndian.PutUint16(header[0:2], id)
	binary.BigEndian.PutUint16(header[2:4], 0x0100)
	binary.BigEndian.PutUint16(header[4:6], 1)

	packet := append(header[:], encodeName(name)...)
	return append(packet, byte(queryType>>8), byte(queryType), 0, 1)
}

func encodeName(name string) []byte {
	parts := strings.Split(name, ".")

	result := make([]byte, 0, len(parts)*2)
	for _, part := range parts {
		result = append(result, byte(len(part)))
		result = append(result, part...)
	}
	result = append(result, 0)

	return result
}

func sendPacket(addr string, packet []byte, id uint16) ([]byte, error) {
	conn, err := net.DialTimeout("udp", addr, time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	response := make([]byte, 65535)
	for {
		n, err := conn.Read(response)
		if err != nil {
			return nil, err
		}

		if n >= dnsHeaderSize && binary.BigEndian.Uint16(response[:dnsUint16Size]) == id {
			return response[:n], nil
		}
	}
}

func parseResponse(message []byte) (dnsResponse, error) {
	if len(message) < dnsHeaderSize {
		return dnsResponse{}, errors.New("DNS: truncated header")
	}
	response := dnsResponse{
		rcode:       binary.BigEndian.Uint16(message[dnsFlagsOffset:dnsQuestionCountOffset]) & dnsRCodeMask,
		answerCount: binary.BigEndian.Uint16(message[dnsAnswerCountOffset : dnsAnswerCountOffset+dnsUint16Size]),
	}
	questionCount := int(binary.BigEndian.Uint16(message[dnsQuestionCountOffset:dnsAnswerCountOffset]))
	offset, err := skipQuestions(message, questionCount)
	if err != nil {
		return dnsResponse{}, err
	}

	for i := 0; i < int(response.answerCount); i++ {
		answer, next, err := readAnswer(message, offset)
		if err != nil {
			return dnsResponse{}, err
		}
		offset = next
		if i == 0 || answer.ttl < response.minTTL {
			response.minTTL = answer.ttl
		}
		if answer.recordType != "" {
			response.answers = append(response.answers, answer)
		}
	}
	return response, nil
}

func skipQuestions(message []byte, count int) (int, error) {
	offset := dnsHeaderSize
	for i := 0; i < count; i++ {
		_, next, err := readName(message, offset)
		if err != nil {
			return 0, err
		}
		offset = next + dnsQuestionFieldsSize
		if offset > len(message) {
			return 0, errors.New("DNS: truncated question")
		}
	}
	return offset, nil
}

func readAnswer(message []byte, offset int) (dnsAnswer, int, error) {
	const (
		ttlOffset        = 4
		dataLengthOffset = 8
	)

	_, start, err := readName(message, offset)
	if err != nil {
		return dnsAnswer{}, 0, err
	}
	if start+dnsRecordHeaderSize > len(message) {
		return dnsAnswer{}, 0, errors.New("DNS: truncated record header")
	}
	header := message[start : start+dnsRecordHeaderSize]
	recordType := binary.BigEndian.Uint16(header[:dnsUint16Size])
	ttl := binary.BigEndian.Uint32(header[ttlOffset:dataLengthOffset])
	length := int(binary.BigEndian.Uint16(header[dataLengthOffset:]))
	start += dnsRecordHeaderSize
	end := start + length
	if end > len(message) {
		return dnsAnswer{}, 0, errors.New("DNS: truncated record data")
	}

	answer, err := parseAnswer(message, start, end, recordType)
	answer.ttl = ttl
	return answer, end, err
}

func parseAnswer(message []byte, start, end int, recordType uint16) (dnsAnswer, error) {
	data := message[start:end]
	var answer dnsAnswer
	var err error

	switch recordType {
	case dnsTypeA:
		answer.recordType = "A"
		answer.value = net.IP(data).String()
	case dnsTypeAAAA:
		answer.recordType = "AAAA"
		answer.value = net.IP(data).String()
	case dnsTypeNS, dnsTypeCNAME:
		answer.recordType = "NS"
		if recordType == dnsTypeCNAME {
			answer.recordType = "CNAME"
		}
		answer.value, _, err = readName(message, start)
	case dnsTypeMX:
		if len(data) < dnsUint16Size {
			return answer, errors.New("truncated MX priority")
		}
		answer.recordType = "MX"
		priority := binary.BigEndian.Uint16(data[:dnsUint16Size])
		name, _, nameErr := readName(message, start+dnsUint16Size)
		answer.value = fmt.Sprintf("%d %s", priority, name)
		err = nameErr
	case dnsTypeTXT:
		answer.recordType = "TXT"
		answer.value, err = readText(data)
	}
	return answer, err
}

func readText(data []byte) (string, error) {
	var text strings.Builder
	for len(data) > 0 {
		length := int(data[0])
		data = data[1:]
		if length > len(data) {
			return "", errors.New("truncated TXT string")
		}
		text.Write(data[:length])
		data = data[length:]
	}
	return text.String(), nil
}

func readName(message []byte, offset int) (string, int, error) {
	labels, next, err := readLabels(message, offset)
	if err != nil {
		return "", 0, err
	}
	return strings.Join(labels, ".") + ".", next, nil
}

func readLabels(message []byte, offset int) ([]string, int, error) {
	const (
		pointerPrefix     = 0xC0
		pointerOffsetMask = 0x3FFF
	)
	var labels []string
	for {
		if offset < 0 || offset >= len(message) {
			return nil, 0, errors.New("DNS: name outside packet")
		}
		length := int(message[offset])
		if length == 0 {
			return labels, offset + 1, nil
		}
		if length&pointerPrefix == pointerPrefix {
			end := offset + dnsUint16Size
			if end > len(message) {
				return nil, 0, errors.New("DNS: truncated pointer")
			}
			target := int(binary.BigEndian.Uint16(message[offset:end]) & pointerOffsetMask)
			tail, _, err := readLabels(message, target)
			return append(labels, tail...), end, err
		}

		offset++
		end := offset + length
		if end > len(message) {
			return nil, 0, errors.New("DNS: invalid label")
		}
		labels = append(labels, string(message[offset:end]))
		offset = end
	}
}

func statusName(responseCode uint16) string {
	if name, ok := map[uint16]string{
		0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN", 5: "REFUSED",
	}[responseCode]; ok {
		return name
	}
	return fmt.Sprintf("RCODE%d", responseCode)
}

func printResponse(response dnsResponse) {
	fmt.Printf("status %s\n", statusName(response.rcode))
	for _, answer := range response.answers {
		fmt.Printf("answer %s %s %d\n", answer.recordType, answer.value, answer.ttl)
	}
	fmt.Println("end")
}
