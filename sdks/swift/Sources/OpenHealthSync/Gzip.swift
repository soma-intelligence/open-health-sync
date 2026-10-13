// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
#if canImport(Compression)
import Compression
#endif

/// Writes gzip (RFC 1952) members that any gunzip reads: a 10-byte header, a deflate body,
/// then the CRC-32 and length of the input. On Apple platforms the body is compressed with
/// the Compression framework; elsewhere it is made of deflate "stored" blocks (valid
/// deflate, not compressed).
public enum Gzip {
    /// One gzip member holding `data`.
    public static func compress(_ data: Data) -> Data {
        let body = deflate(data)
        var out = Data()
        out.reserveCapacity(body.count + 18)
        // ID1 ID2, CM=8 (deflate), FLG=0, MTIME=0 (none), XFL=0, OS=255 (unknown).
        out.append(contentsOf: [0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff] as [UInt8])
        out.append(body)
        appendLittleEndian(&out, crc32(data))
        appendLittleEndian(&out, UInt32(truncatingIfNeeded: data.count))
        return out
    }

    /// The CRC-32 (IEEE 802.3, the one gzip uses) of `data`.
    public static func crc32(_ data: Data) -> UInt32 {
        let table = crcTable
        var crc: UInt32 = 0xffff_ffff
        data.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
            for byte in raw {
                crc = table[Int((crc ^ UInt32(byte)) & 0xff)] ^ (crc >> 8)
            }
        }
        return ~crc
    }

    static let crcTable: [UInt32] = (0..<256).map { (n: Int) -> UInt32 in
        var c = UInt32(n)
        for _ in 0..<8 {
            c = (c & 1) != 0 ? (0xedb8_8320 ^ (c >> 1)) : (c >> 1)
        }
        return c
    }

    static func deflate(_ data: Data) -> Data {
        #if canImport(Compression)
        if let compressed = deflateWithCompressionFramework(data) {
            return compressed
        }
        #endif
        return deflateStored(data)
    }

    /// Raw deflate made only of stored blocks, at most 65535 bytes each.
    static func deflateStored(_ data: Data) -> Data {
        let bytes = [UInt8](data)
        var out = Data()
        out.reserveCapacity(bytes.count + (bytes.count / 65_535 + 1) * 5)
        var offset = 0
        repeat {
            let n = min(65_535, bytes.count - offset)
            let isFinal = offset + n >= bytes.count
            // BFINAL in bit 0, BTYPE=00 (stored) in bits 1-2, then padding to the byte.
            out.append(isFinal ? 0x01 : 0x00)
            let len = UInt16(n)
            let nlen = ~len
            out.append(UInt8(len & 0xff))
            out.append(UInt8(len >> 8))
            out.append(UInt8(nlen & 0xff))
            out.append(UInt8(nlen >> 8))
            if n > 0 {
                out.append(contentsOf: bytes[offset..<(offset + n)])
            }
            offset += n
        } while offset < bytes.count
        return out
    }

    #if canImport(Compression)
    /// COMPRESSION_ZLIB writes raw deflate (RFC 1951) without a zlib header.
    static func deflateWithCompressionFramework(_ data: Data) -> Data? {
        if data.isEmpty {
            return nil
        }
        let capacity = data.count + data.count / 8 + 1024
        var destination = [UInt8](repeating: 0, count: capacity)
        let written: Int = data.withUnsafeBytes { (src: UnsafeRawBufferPointer) -> Int in
            guard let source = src.bindMemory(to: UInt8.self).baseAddress else {
                return 0
            }
            return destination.withUnsafeMutableBufferPointer { (dst: inout UnsafeMutableBufferPointer<UInt8>) -> Int in
                guard let target = dst.baseAddress else {
                    return 0
                }
                return compression_encode_buffer(target, capacity, source, data.count, nil, COMPRESSION_ZLIB)
            }
        }
        if written <= 0 || written >= capacity {
            return nil
        }
        return Data(destination[0..<written])
    }
    #endif

    static func appendLittleEndian(_ out: inout Data, _ value: UInt32) {
        out.append(UInt8(value & 0xff))
        out.append(UInt8((value >> 8) & 0xff))
        out.append(UInt8((value >> 16) & 0xff))
        out.append(UInt8((value >> 24) & 0xff))
    }
}
