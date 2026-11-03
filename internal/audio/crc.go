package audio

// CRC tables used by the FLAC frame layer (FLAC spec §8, §9).
//
// Frame header CRC-8 : polynomial x^8+x^2+x+1  (0x07)
// Frame      CRC-16  : polynomial x^16+x^15+x^2+1 (0x8005)
// Both start from zero and are not reflected.

var crc8Table [256]byte
var crc16Table [256]uint16

func init() {
	for i := 0; i < 256; i++ {
		c := byte(i)
		for bit := 0; bit < 8; bit++ {
			if c&0x80 != 0 {
				c = (c << 1) ^ 0x07
			} else {
				c <<= 1
			}
		}
		crc8Table[i] = c

		v := uint16(i) << 8
		for bit := 0; bit < 8; bit++ {
			if v&0x8000 != 0 {
				v = (v << 1) ^ 0x8005
			} else {
				v <<= 1
			}
		}
		crc16Table[i] = v
	}
}

// crc8 returns the FLAC frame-header CRC-8 of b.
func crc8(b []byte) byte {
	var c byte
	for _, x := range b {
		c = crc8Table[c^x]
	}
	return c
}

// crc16 returns the FLAC frame CRC-16 of b.
func crc16(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c = (c << 8) ^ crc16Table[byte(c>>8)^x]
	}
	return c
}
