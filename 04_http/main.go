package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var (
	errStartLine = errors.New("bad_start_line")
	errHeader    = errors.New("bad_header")
	errBody      = errors.New("incomplete_body")
	errChunk     = errors.New("bad_chunk")
)

type header struct {
	name, value string
}

type message struct {
	method, target string
	status, reason string
	headers        []header
	body           []byte
}

func main() {
	if err := run(); err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
}

func run() error {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	msg, err := parseMessage(data)
	if err != nil {
		return err
	}
	printMessage(msg)
	return nil
}

func parseMessage(data []byte) (message, error) {
	var msg message
	line, rest, ok := readLine(data)
	if !ok {
		return msg, errStartLine
	}
	if err := parseStartLine(string(line), &msg); err != nil {
		return msg, err
	}

	contentLength := 0
	chunked := false
	for {
		line, rest, ok = readLine(rest)
		if !ok {
			return msg, errHeader
		}
		if len(line) == 0 {
			break
		}
		field, err := parseHeader(string(line))
		if err != nil {
			return msg, err
		}
		switch field.name {
		case "content-length":
			if !isnum(field.value) {
				return msg, errHeader
			}
			length, err := strconv.Atoi(field.value)
			if err != nil {
				return msg, errHeader
			}
			contentLength = length
		case "transfer-encoding":
			chunked = chunked || strings.EqualFold(field.value, "chunked")
		}
		msg.headers = append(msg.headers, field)
	}

	if chunked {
		body, err := parseChunks(rest)
		if err != nil {
			return msg, err
		}
		msg.body = body
	} else {
		if len(rest) < contentLength {
			return msg, errBody
		}
		msg.body = rest[:contentLength]
	}
	return msg, nil
}

func parseStartLine(line string, msg *message) error {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return errStartLine
	}
	if strings.HasPrefix(line, "HTTP/") {
		if parts[0] != "HTTP/1.1" || len(parts[1]) != 3 || !isnum(parts[1]) {
			return errStartLine
		}
		msg.status, msg.reason = parts[1], parts[2]
	} else {
		if !token(parts[0]) || parts[1] == "" || parts[2] != "HTTP/1.1" {
			return errStartLine
		}
		msg.method, msg.target = parts[0], parts[1]
	}
	return nil
}

func parseHeader(line string) (header, error) {
	name, value, ok := strings.Cut(line, ":")
	if !ok || !token(name) {
		return header{}, errHeader
	}
	return header{strings.ToLower(name), strings.Trim(value, " \t")}, nil
}

func parseChunks(data []byte) ([]byte, error) {
	var body []byte
	for {
		line, rest, ok := readLine(data)
		if !ok {
			return nil, errChunk
		}
		sizeText, _, _ := strings.Cut(string(line), ";")
		size, err := strconv.ParseUint(sizeText, 16, 64)
		if err != nil {
			return nil, errChunk
		}
		if size == 0 {
			return body, nil
		}
		if len(rest) < 2 || size > uint64(len(rest)-2) {
			return nil, errChunk
		}
		n := int(size)
		if !bytes.Equal(rest[n:n+2], []byte("\r\n")) {
			return nil, errChunk
		}
		body = append(body, rest[:n]...)
		data = rest[n+2:]
	}
}

func readLine(data []byte) ([]byte, []byte, bool) {
	return bytes.Cut(data, []byte("\r\n"))
}

func token(text string) bool {
	if text == "" {
		return false
	}
	for _, char := range text {
		valid := char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", char)
		if !valid {
			return false
		}
	}
	return true
}

func isnum(text string) bool {
	return text != "" && strings.Trim(text, "0123456789") == ""
}

func printMessage(msg message) {
	if msg.status != "" {
		fmt.Println("type response")
		fmt.Println("version HTTP/1.1")
		fmt.Println("status", msg.status)
		fmt.Println("reason", msg.reason)
	} else {
		fmt.Println("type request")
		fmt.Println("method", msg.method)
		fmt.Println("target", msg.target)
		fmt.Println("version HTTP/1.1")
	}
	for _, field := range msg.headers {
		fmt.Println("header", field.name, field.value)
	}
	fmt.Println("body.length", len(msg.body))
	if printable(msg.body) {
		fmt.Println("body.text", string(msg.body))
	}
}

func printable(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	for _, b := range body {
		if b < 0x20 || b > 0x7E {
			return false
		}
	}
	return true
}
