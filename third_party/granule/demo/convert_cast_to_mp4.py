#!/usr/bin/env python3
"""
convert_cast_to_mp4.py - Converts an asciinema .cast recording to SVG and MP4 video.
"""

import os
import shutil
import subprocess
import sys
import tempfile

DRACULA_TEMPLATE = """<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" id="terminal" baseProfile="full" viewBox="0 0 960 540" width="960" version="1.1">
    <defs>
        <termtosvg:template_settings xmlns:termtosvg="https://github.com/nbedos/termtosvg">
            <termtosvg:screen_geometry columns="110" rows="36"/>
            <termtosvg:animation type="css"/>
        </termtosvg:template_settings>
        <style type="text/css" id="generated-style"></style>
        <style type="text/css" id="user-style">
            .foreground {fill: #f8f8f2;}
            .background {fill: #282a36;}
            .color0 {fill: #21222c;}
            .color1 {fill: #ff5555;}
            .color2 {fill: #50fa7b;}
            .color3 {fill: #f1fa8c;}
            .color4 {fill: #bd93f9;}
            .color5 {fill: #ff79c6;}
            .color6 {fill: #8be9fd;}
            .color7 {fill: #f8f8f2;}
            .color8 {fill: #6272a4;}
            .color9 {fill: #ff6e6e;}
            .color10 {fill: #69ff94;}
            .color11 {fill: #ffffa5;}
            .color12 {fill: #d6acff;}
            .color13 {fill: #ff92df;}
            .color14 {fill: #a4ffff;}
            .color15 {fill: #ffffff;}
        </style>
    </defs>
    <svg id="screen" width="960" height="540" viewBox="0 0 960 540" preserveAspectRatio="xMidYMin slice"></svg>
</svg>
"""

def main():
    if len(sys.argv) < 3:
        print("Usage: convert_cast_to_mp4.py <input.cast> <output.mp4> [output.svg]")
        sys.exit(1)

    input_cast = os.path.abspath(sys.argv[1])
    output_mp4 = os.path.abspath(sys.argv[2])
    output_svg = os.path.abspath(sys.argv[3]) if len(sys.argv) > 3 else None

    if not os.path.exists(input_cast):
        print(f"Error: input file {input_cast} does not exist")
        sys.exit(1)

    termtosvg_bin = shutil.which("termtosvg") or os.path.expanduser("~/.local/bin/termtosvg")

    with tempfile.NamedTemporaryFile(mode="w", suffix=".svg", delete=False) as template_f:
        template_f.write(DRACULA_TEMPLATE)
        template_path = template_f.name

    try:
        # Render standalone SVG if requested
        if output_svg:
            print(f"Rendering animated SVG to {output_svg}...")
            subprocess.run([termtosvg_bin, "render", input_cast, output_svg, "-t", template_path, "-M", "2000"], check=True)

        # Render still frames and convert to MP4
        temp_dir = tempfile.mkdtemp(prefix="termtosvg_frames_")
        png_dir = tempfile.mkdtemp(prefix="termtosvg_pngs_")

        try:
            print("Rendering animation frames...")
            subprocess.run([termtosvg_bin, "render", input_cast, temp_dir, "-s", "-t", template_path, "-m", "100", "-M", "1000"], check=True)

            svg_files = sorted([f for f in os.listdir(temp_dir) if f.endswith(".svg")])
            if not svg_files:
                print("No SVG frames rendered.")
                sys.exit(1)

            print(f"Converting {len(svg_files)} SVG frames to PNG...")
            for i, svg_file in enumerate(svg_files):
                svg_path = os.path.join(temp_dir, svg_file)
                png_path = os.path.join(png_dir, f"frame_{i:05d}.png")
                subprocess.run(["rsvg-convert", "-w", "1280", "-h", "720", "-b", "#282a36", svg_path, "-o", png_path], check=True)

            print(f"Encoding MP4 video to {output_mp4} with ffmpeg...")
            ffmpeg_cmd = [
                "ffmpeg", "-y",
                "-framerate", "10",
                "-i", os.path.join(png_dir, "frame_%05d.png"),
                "-c:v", "libx264",
                "-profile:v", "high",
                "-crf", "20",
                "-pix_fmt", "yuv420p",
                output_mp4
            ]
            subprocess.run(ffmpeg_cmd, check=True)
            print(f"Successfully created {output_mp4} ({os.path.getsize(output_mp4)} bytes)")

        finally:
            shutil.rmtree(temp_dir, ignore_errors=True)
            shutil.rmtree(png_dir, ignore_errors=True)
    finally:
        if os.path.exists(template_path):
            os.remove(template_path)

if __name__ == "__main__":
    main()
