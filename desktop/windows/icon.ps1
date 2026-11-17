# Reproduce the macOS icon.swift F/status-dot mark as a Windows multi-size ICO.
# This is an asset maintenance tool; normal cargo builds use assets/runner.ico.
[CmdletBinding()]
param(
    [string]$OutputPath = (Join-Path $PSScriptRoot 'assets/runner.ico'),
    [string]$PreviewPath
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

function New-RoundedTile {
    $path = [System.Drawing.Drawing2D.GraphicsPath]::new()
    $path.AddArc(100, 100, 368, 368, 180, 90)
    $path.AddArc(556, 100, 368, 368, 270, 90)
    $path.AddArc(556, 556, 368, 368, 0, 90)
    $path.AddArc(100, 556, 368, 368, 90, 90)
    $path.CloseFigure()
    return $path
}

$bitmap = [System.Drawing.Bitmap]::new(1024, 1024)
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
$tile = New-RoundedTile
$gradient = [System.Drawing.Drawing2D.LinearGradientBrush]::new(
    [System.Drawing.RectangleF]::new(100, 100, 824, 824),
    [System.Drawing.Color]::FromArgb(94, 102, 237),
    [System.Drawing.Color]::FromArgb(31, 33, 89),
    [single]62
)
$mark = [System.Drawing.Drawing2D.GraphicsPath]::new()
$pen = [System.Drawing.Pen]::new([System.Drawing.Color]::White, 86)
$dot = [System.Drawing.SolidBrush]::new([System.Drawing.Color]::FromArgb(173, 209, 255))
try {
    $graphics.Clear([System.Drawing.Color]::Transparent)
    $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $graphics.FillPath($gradient, $tile)
    $mark.AddLine(358, 700, 358, 324)
    $mark.AddLine(358, 324, 666, 324)
    $mark.StartFigure()
    $mark.AddLine(358, 512, 532, 512)
    $pen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
    $pen.EndCap = [System.Drawing.Drawing2D.LineCap]::Round
    $pen.LineJoin = [System.Drawing.Drawing2D.LineJoin]::Round
    $graphics.DrawPath($pen, $mark)
    $graphics.FillEllipse($dot, 620, 466, 92, 92)
    if ($PreviewPath) {
        $bitmap.Save($PreviewPath, [System.Drawing.Imaging.ImageFormat]::Png)
    }

    $sizes = @(16, 20, 24, 32, 40, 48, 64, 128, 256)
    $frames = foreach ($size in $sizes) {
        $frame = [System.Drawing.Bitmap]::new($size, $size)
        $scaled = [System.Drawing.Graphics]::FromImage($frame)
        $png = [System.IO.MemoryStream]::new()
        try {
            $scaled.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
            $scaled.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
            $scaled.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
            $scaled.DrawImage($bitmap, 0, 0, $size, $size)
            $frame.Save($png, [System.Drawing.Imaging.ImageFormat]::Png)
            # Keep each PNG as one frame instead of enumerating its bytes.
            ,$png.ToArray()
        }
        finally {
            $png.Dispose()
            $scaled.Dispose()
            $frame.Dispose()
        }
    }
    $ico = [System.IO.MemoryStream]::new()
    $writer = [System.IO.BinaryWriter]::new($ico)
    try {
        $writer.Write([uint16]0)
        $writer.Write([uint16]1)
        $writer.Write([uint16]$sizes.Count)
        $offset = 6 + 16 * $sizes.Count
        for ($index = 0; $index -lt $sizes.Count; $index++) {
            $dimension = if ($sizes[$index] -eq 256) { 0 } else { $sizes[$index] }
            $writer.Write([byte]$dimension)
            $writer.Write([byte]$dimension)
            $writer.Write([byte]0)
            $writer.Write([byte]0)
            $writer.Write([uint16]1)
            $writer.Write([uint16]32)
            $writer.Write([uint32]$frames[$index].Length)
            $writer.Write([uint32]$offset)
            $offset += $frames[$index].Length
        }
        foreach ($frame in $frames) { $writer.Write([byte[]]$frame) }
        New-Item -ItemType Directory -Force (Split-Path -Parent $OutputPath) | Out-Null
        [System.IO.File]::WriteAllBytes($OutputPath, $ico.ToArray())
    }
    finally {
        $writer.Dispose()
        $ico.Dispose()
    }
}
finally {
    $dot.Dispose()
    $pen.Dispose()
    $mark.Dispose()
    $gradient.Dispose()
    $tile.Dispose()
    $graphics.Dispose()
    $bitmap.Dispose()
}
