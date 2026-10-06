package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

type frame struct {
	ethernet      []byte
	ip            []byte
	transport     []byte
	payloadLength int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	dump := strings.Join(strings.Fields(string(input)), "")
	data, err := hex.DecodeString(strings.ReplaceAll(dump, ":", ""))
	if err != nil {
		return fmt.Errorf("hex input: %w", err)
	}
	packet, err := parseFrame(data)
	if err != nil {
		return err
	}
	printFrame(packet)
	return nil
}

func parseFrame(data []byte) (frame, error) {
	var packet frame
	if len(data) < 14 {
		return packet, errors.New("Ethernet: truncated header")
	}
	packet.ethernet = data[:14]
	if binary.BigEndian.Uint16(data[12:14]) != 0x0800 {
		return packet, nil
	}

	data = data[14:]
	if len(data) < 20 {
		return packet, errors.New("IPv4: invalid header")
	}
	headerLength := int(data[0]&0x0F) * 4
	totalLength := int(binary.BigEndian.Uint16(data[2:4]))
	if headerLength < 20 || totalLength < headerLength || totalLength > len(data) {
		return packet, errors.New("IPv4: invalid length")
	}
	packet.ip = data[:headerLength]
	data = data[headerLength:totalLength]
	packet.payloadLength = len(data)
	if binary.BigEndian.Uint16(packet.ip[6:8])&0x1FFF != 0 {
		return packet, nil
	}

	switch packet.ip[9] {
	case 6:
		if len(data) < 20 {
			return packet, errors.New("TCP: truncated header")
		}
		headerLength = int(data[12]>>4) * 4
		if headerLength < 20 || headerLength > len(data) {
			return packet, errors.New("TCP: invalid header length")
		}
		packet.transport = data[:headerLength]
		packet.payloadLength = len(data) - headerLength
	case 17:
		if len(data) < 8 {
			return packet, errors.New("UDP: truncated header")
		}
		length := int(binary.BigEndian.Uint16(data[4:6]))
		if length < 8 {
			return packet, errors.New("UDP: invalid length")
		}
		packet.transport = data[:8]
		packet.payloadLength = min(length, len(data)) - 8
	}
	return packet, nil
}

func checksumValid(header []byte) bool {
	var sum uint32
	for i := 0; i < len(header); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return sum == 0xFFFF
}

func printFrame(packet frame) {
	eth := packet.ethernet
	fmt.Println("eth.dst", net.HardwareAddr(eth[:6]))
	fmt.Println("eth.src", net.HardwareAddr(eth[6:12]))
	fmt.Printf("eth.ethertype 0x%04x\n", binary.BigEndian.Uint16(eth[12:14]))
	if packet.ip == nil {
		return
	}
	printIPv4(packet.ip)
	if packet.transport != nil {
		switch packet.ip[9] {
		case 6:
			printTCP(packet.transport)
		case 17:
			printUDP(packet.transport)
		}
	}
	fmt.Println("payload.length", packet.payloadLength)
}

func printIPv4(ip []byte) {
	flags := []string{"none", "MF", "DF", "DF,MF"}
	fmt.Println("ip.version", ip[0]>>4)
	fmt.Println("ip.ihl_bytes", len(ip))
	fmt.Println("ip.total_length", binary.BigEndian.Uint16(ip[2:4]))
	fmt.Printf("ip.id 0x%04x\n", binary.BigEndian.Uint16(ip[4:6]))
	fmt.Println("ip.flags", flags[(ip[6]>>5)&3])
	fmt.Println("ip.frag_offset", int(binary.BigEndian.Uint16(ip[6:8])&0x1FFF)*8)
	fmt.Println("ip.ttl", ip[8])
	fmt.Println("ip.protocol", ip[9])
	fmt.Println("ip.src", net.IP(ip[12:16]))
	fmt.Println("ip.dst", net.IP(ip[16:20]))
	fmt.Println("ip.checksum_valid", checksumValid(ip))
}

func printTCP(tcp []byte) {
	fmt.Println("tcp.src_port", binary.BigEndian.Uint16(tcp[:2]))
	fmt.Println("tcp.dst_port", binary.BigEndian.Uint16(tcp[2:4]))
	fmt.Println("tcp.seq", binary.BigEndian.Uint32(tcp[4:8]))
	fmt.Println("tcp.ack", binary.BigEndian.Uint32(tcp[8:12]))
	fmt.Println("tcp.data_offset_bytes", len(tcp))
	fmt.Println("tcp.flags", tcpFlags(tcp[13]))
	fmt.Println("tcp.window", binary.BigEndian.Uint16(tcp[14:16]))
}

func printUDP(udp []byte) {
	fmt.Println("udp.src_port", binary.BigEndian.Uint16(udp[:2]))
	fmt.Println("udp.dst_port", binary.BigEndian.Uint16(udp[2:4]))
	fmt.Println("udp.length", binary.BigEndian.Uint16(udp[4:6]))
}

func tcpFlags(bits byte) string {
	var flags []string
	for bit, name := range []string{"FIN", "SYN", "RST", "PSH", "ACK", "URG"} {
		if bits&(1<<bit) != 0 {
			flags = append(flags, name)
		}
	}
	if len(flags) == 0 {
		return "none"
	}
	return strings.Join(flags, ",")
}
