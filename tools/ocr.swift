// tools/ocr.swift — on-device OCR for emulator screenshots using the
// macOS Vision framework. No tesseract / no network needed.
//
// Usage:
//   swiftc -O tools/ocr.swift -o /tmp/ocr 2>/dev/null
//   /tmp/ocr /path/to/screen.png
//
// Output: one line per recognized text region, sorted top-to-bottom:
//   x,y,w,h | text
// Coordinates are in the screenshot's pixel space (0,0 = top-left),
// matching the bot's coordinate conventions.

import Foundation
import Vision
import AppKit

guard CommandLine.arguments.count > 1 else {
    FileHandle.standardError.write(Data("usage: ocr <image>\n".utf8))
    exit(2)
}

let path = CommandLine.arguments[1]
guard let img = NSImage(contentsOfFile: path),
      let cg = img.cgImage(forProposedRect: nil, context: nil, hints: nil) else {
    FileHandle.standardError.write(Data("cannot load image: \(path)\n".utf8))
    exit(1)
}

let W = cg.width
let H = cg.height

let request = VNRecognizeTextRequest()
request.recognitionLevel = .accurate
request.usesLanguageCorrection = false
request.recognitionLanguages = ["en-US"]

let handler = VNImageRequestHandler(cgImage: cg, options: [:])
do {
    try handler.perform([request])
} catch {
    FileHandle.standardError.write(Data("vision error: \(error)\n".utf8))
    exit(1)
}

struct Line {
    let x: Int, y: Int, w: Int, h: Int, text: String
}

var lines: [Line] = []
for obs in request.results ?? [] {
    guard let cand = obs.topCandidates(1).first else { continue }
    let bb = obs.boundingBox // normalized, origin bottom-left
    let x = Int(bb.origin.x * CGFloat(W))
    let y = Int((1.0 - bb.origin.y - bb.height) * CGFloat(H))
    let w = Int(bb.width * CGFloat(W))
    let h = Int(bb.height * CGFloat(H))
    lines.append(Line(x: x, y: y, w: w, h: h, text: cand.string))
}

// Sort top-to-bottom, then left-to-right.
lines.sort { a, b in
    if abs(a.y - b.y) > 4 { return a.y < b.y }
    return a.x < b.x
}

for l in lines {
    let t = l.text.replacingOccurrences(of: "\n", with: " ")
    print("\(l.x),\(l.y),\(l.w),\(l.h) | \(t)")
}
