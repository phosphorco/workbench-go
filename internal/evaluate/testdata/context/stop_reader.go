package main

import (
	"bufio"
	"io"
	"os"
	"time"
)

func main() {
	reader := bufio.NewReader(os.Stdin)
	if err := skipValue(reader); err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.Write([]byte("\x92\x21\x83\xa9requestId\x01\xab" + "evaluatorId" + "\x07\xa5error\xa0"))
	time.Sleep(30 * time.Second)
}

func skipValue(reader *bufio.Reader) error {
	code, err := reader.ReadByte()
	if err != nil {
		return err
	}
	switch {
	case code <= 0x7f || code >= 0xe0 || code == 0xc0 || code == 0xc2 || code == 0xc3:
		return nil
	case code >= 0xa0 && code <= 0xbf:
		return discard(reader, int(code&0x1f))
	case code >= 0x90 && code <= 0x9f:
		return skipValues(reader, int(code&0x0f))
	case code >= 0x80 && code <= 0x8f:
		return skipValues(reader, int(code&0x0f)*2)
	case code == 0xcc || code == 0xd0:
		return discard(reader, 1)
	case code == 0xcd || code == 0xd1:
		return discard(reader, 2)
	case code == 0xce || code == 0xd2 || code == 0xca:
		return discard(reader, 4)
	case code == 0xcf || code == 0xd3 || code == 0xcb:
		return discard(reader, 8)
	case code == 0xd9 || code == 0xc4:
		return discardSized(reader, 1)
	case code == 0xda || code == 0xc5:
		return discardSized(reader, 2)
	case code == 0xdb || code == 0xc6:
		return discardSized(reader, 4)
	case code == 0xdc:
		count, err := readUint(reader, 2)
		if err != nil {
			return err
		}
		return skipValues(reader, int(count))
	case code == 0xdd:
		count, err := readUint(reader, 4)
		if err != nil {
			return err
		}
		return skipValues(reader, int(count))
	case code == 0xde:
		count, err := readUint(reader, 2)
		if err != nil {
			return err
		}
		return skipValues(reader, int(count)*2)
	case code == 0xdf:
		count, err := readUint(reader, 4)
		if err != nil {
			return err
		}
		return skipValues(reader, int(count)*2)
	default:
		return discard(reader, 1)
	}
}

func skipValues(reader *bufio.Reader, count int) error {
	for index := 0; index < count; index++ {
		if err := skipValue(reader); err != nil {
			return err
		}
	}
	return nil
}

func discardSized(reader *bufio.Reader, width int) error {
	length, err := readUint(reader, width)
	if err != nil {
		return err
	}
	return discard(reader, int(length))
}

func readUint(reader *bufio.Reader, width int) (uint64, error) {
	var value uint64
	for index := 0; index < width; index++ {
		byteValue, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		value = value<<8 | uint64(byteValue)
	}
	return value, nil
}

func discard(reader *bufio.Reader, count int) error {
	_, err := io.CopyN(io.Discard, reader, int64(count))
	return err
}
