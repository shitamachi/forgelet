import AppKit

guard CommandLine.arguments.count == 2,
      let bitmap = NSBitmapImageRep(
          bitmapDataPlanes: nil, pixelsWide: 1024, pixelsHigh: 1024,
          bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
          colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0
      ),
      let context = NSGraphicsContext(bitmapImageRep: bitmap)
else {
    throw CocoaError(.fileWriteUnknown)
}

NSGraphicsContext.current = context
let tile = NSBezierPath(roundedRect: NSRect(x: 100, y: 100, width: 824, height: 824),
                        xRadius: 184, yRadius: 184)
NSGraphicsContext.saveGraphicsState()
let shadow = NSShadow()
shadow.shadowColor = NSColor(srgbRed: 0.07, green: 0.08, blue: 0.25, alpha: 0.32)
shadow.shadowBlurRadius = 24
shadow.shadowOffset = NSSize(width: 0, height: -16)
shadow.set()
NSColor(srgbRed: 0.13, green: 0.15, blue: 0.35, alpha: 1).setFill()
tile.fill()
NSGraphicsContext.restoreGraphicsState()

NSGradient(
    starting: NSColor(srgbRed: 0.37, green: 0.40, blue: 0.93, alpha: 1),
    ending: NSColor(srgbRed: 0.12, green: 0.13, blue: 0.35, alpha: 1)
)?.draw(in: tile, angle: -62)

// Carry the workbench's F and blue status dot into the Dock.
let mark = NSBezierPath()
mark.lineWidth = 86
mark.lineCapStyle = .round
mark.lineJoinStyle = .round
mark.move(to: NSPoint(x: 358, y: 324))
mark.line(to: NSPoint(x: 358, y: 700))
mark.line(to: NSPoint(x: 666, y: 700))
mark.move(to: NSPoint(x: 358, y: 512))
mark.line(to: NSPoint(x: 532, y: 512))
NSColor.white.setStroke()
mark.stroke()

NSColor(srgbRed: 0.68, green: 0.82, blue: 1, alpha: 1).setFill()
NSBezierPath(ovalIn: NSRect(x: 620, y: 466, width: 92, height: 92)).fill()
NSGraphicsContext.current = nil

guard let data = bitmap.representation(using: .png, properties: [:]) else {
    throw CocoaError(.fileWriteUnknown)
}
try data.write(to: URL(fileURLWithPath: CommandLine.arguments[1]))
