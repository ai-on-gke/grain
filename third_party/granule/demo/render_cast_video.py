#!/usr/bin/env python3
"""
render_cast_video.py - High quality terminal video renderer from asciicast .cast file.
Simulates terminal screen states with pyte and renders 1080p video with ffmpeg.
"""

import json
import subprocess
import sys
import pyte
from PIL import Image, ImageDraw, ImageFont

# 1080p geometry
WIDTH = 1920
HEIGHT = 1080
COLS = 110
ROWS = 36
FPS = 15

# Dracula color palette
COLOR_BG = (40, 42, 54)            # #282a36
COLOR_FG = (248, 248, 242)         # #f8f8f2
COLOR_BLACK = (33, 34, 44)         # #21222c
COLOR_RED = (255, 85, 85)           # #ff5555
COLOR_GREEN = (80, 250, 123)       # #50fa7b
COLOR_YELLOW = (241, 250, 140)     # #f1fa8c
COLOR_BLUE = (189, 147, 249)       # #bd93f9
COLOR_MAGENTA = (255, 121, 198)    # #ff79c6
COLOR_CYAN = (139, 233, 253)       # #8be9fd
COLOR_WHITE = (248, 248, 242)      # #f8f8f2
COLOR_BRIGHT_BLACK = (98, 114, 164)# #6272a4
COLOR_BRIGHT_RED = (255, 110, 110) # #ff6e6e
COLOR_BRIGHT_GREEN = (105, 255, 148)# #69ff94
COLOR_BRIGHT_YELLOW = (255, 255, 165)# #ffffa5
COLOR_BRIGHT_BLUE = (214, 172, 255)# #d6acff
COLOR_BRIGHT_MAGENTA = (255, 146, 223)# #ff92df
COLOR_BRIGHT_CYAN = (164, 255, 255)# #a4ffff
COLOR_BRIGHT_WHITE = (255, 255, 255)# #ffffff

COLOR_BAR = (33, 34, 44)
COLOR_BTN_RED = (255, 95, 86)
COLOR_BTN_YEL = (255, 189, 46)
COLOR_BTN_GRN = (39, 201, 63)

COLOR_MAP = {
    "default": COLOR_FG,
    "black": COLOR_BLACK,
    "red": COLOR_RED,
    "green": COLOR_GREEN,
    "yellow": COLOR_YELLOW,
    "brown": COLOR_YELLOW,
    "blue": COLOR_BLUE,
    "magenta": COLOR_MAGENTA,
    "cyan": COLOR_CYAN,
    "white": COLOR_WHITE,
    "brightblack": COLOR_BRIGHT_BLACK,
    "brightred": COLOR_BRIGHT_RED,
    "brightgreen": COLOR_BRIGHT_GREEN,
    "brightyellow": COLOR_BRIGHT_YELLOW,
    "brightbrown": COLOR_BRIGHT_YELLOW,
    "brightblue": COLOR_BRIGHT_BLUE,
    "brightmagenta": COLOR_BRIGHT_MAGENTA,
    "brightcyan": COLOR_BRIGHT_CYAN,
    "brightwhite": COLOR_BRIGHT_WHITE,
}

def get_color(color_name, default=COLOR_FG, is_bold=False):
    if not color_name or color_name == "default":
        return default
    if is_bold and f"bright{color_name}" in COLOR_MAP:
        return COLOR_MAP[f"bright{color_name}"]
    if color_name in COLOR_MAP:
        return COLOR_MAP[color_name]
    if isinstance(color_name, str) and len(color_name) == 6:
        try:
            return tuple(int(color_name[i:i+2], 16) for i in (0, 2, 4))
        except ValueError:
            pass
    return default

def main():
    if len(sys.argv) < 3:
        print("Usage: render_cast_video.py <input.cast> <output.mp4>")
        sys.exit(1)

    input_cast = sys.argv[1]
    output_mp4 = sys.argv[2]

    # Load asciicast events
    events = []
    header = None
    with open(input_cast, "r") as f:
        first = True
        for line in f:
            line = line.strip()
            if not line:
                continue
            if first:
                header = json.loads(line)
                first = False
            else:
                try:
                    events.append(json.loads(line))
                except json.JSONDecodeError:
                    continue

    if not events:
        print("No events in cast file.")
        sys.exit(1)

    total_duration = events[-1][0] + 3.0 # extra 3 seconds pause at end
    total_frames = int(total_duration * FPS)

    print(f"Rendering {total_frames} frames ({total_duration:.1f}s) at {FPS} FPS...")

    # Initialize terminal emulator
    screen = pyte.Screen(COLS, ROWS)
    stream = pyte.Stream(screen)

    # Setup fonts
    font_mono_path = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
    font_bold_path = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"
    title_font_path = "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf"
    
    font_mono = ImageFont.truetype(font_mono_path, 22)
    font_bold = ImageFont.truetype(font_bold_path, 22)
    title_font = ImageFont.truetype(title_font_path, 18)

    char_w = 15
    char_h = 26
    top_bar_h = 48
    margin_left = (WIDTH - (COLS * char_w)) // 2
    margin_top = top_bar_h + 16

    # Launch ffmpeg pipe
    ffmpeg_cmd = [
        "ffmpeg", "-y",
        "-f", "rawvideo",
        "-vcodec", "rawvideo",
        "-s", f"{WIDTH}x{HEIGHT}",
        "-pix_fmt", "rgb24",
        "-r", str(FPS),
        "-i", "-",
        "-c:v", "libx264",
        "-pix_fmt", "yuv420p",
        "-crf", "18",
        "-preset", "fast",
        output_mp4
    ]

    proc = subprocess.Popen(ffmpeg_cmd, stdin=subprocess.PIPE)

    event_idx = 0
    cur_time = 0.0

    for frame_idx in range(total_frames):
        cur_time = frame_idx / FPS

        # Apply all events up to cur_time
        while event_idx < len(events) and events[event_idx][0] <= cur_time:
            ev = events[event_idx]
            if len(ev) >= 3 and ev[1] == "o":
                stream.feed(ev[2])
            event_idx += 1

        # Render frame
        img = Image.new("RGB", (WIDTH, HEIGHT), COLOR_BG)
        draw = ImageDraw.Draw(img)

        # Window Title Bar
        draw.rectangle([(0, 0), (WIDTH, top_bar_h)], fill=COLOR_BAR)
        # Window buttons
        draw.ellipse([(24, 16), (40, 32)], fill=COLOR_BTN_RED)
        draw.ellipse([(48, 16), (64, 32)], fill=COLOR_BTN_YEL)
        draw.ellipse([(72, 16), (88, 32)], fill=COLOR_BTN_GRN)
        # Title
        title_text = "Granule Kubernetes Demo — Multi-VM Pod & Ephemeral Containers"
        draw.text((108, 14), title_text, fill=(189, 147, 249), font=title_font)

        # Render terminal buffer
        for y in range(ROWS):
            line = screen.buffer[y]
            row_y = margin_top + y * char_h
            for x in range(COLS):
                char = line[x]
                if char.data and char.data != ' ':
                    col_x = margin_left + x * char_w
                    fg_color = get_color(char.fg, COLOR_FG, is_bold=char.bold)
                    bg_color = get_color(char.bg, None, is_bold=False)
                    if char.reverse:
                        fg_color, bg_color = (bg_color or COLOR_BG), fg_color
                    if bg_color:
                        draw.rectangle([(col_x, row_y), (col_x + char_w, row_y + char_h)], fill=bg_color)
                    font_to_use = font_bold if char.bold else font_mono
                    draw.text((col_x, row_y), char.data, fill=fg_color, font=font_to_use)

        # Cursor
        if screen.cursor.y < ROWS and screen.cursor.x < COLS:
            cx = margin_left + screen.cursor.x * char_w
            cy = margin_top + screen.cursor.y * char_h
            if (frame_idx // (FPS // 2)) % 2 == 0: # Blink
                draw.rectangle([(cx, cy), (cx + char_w, cy + char_h)], fill=(139, 233, 253, 128))

        proc.stdin.write(img.tobytes())

    proc.stdin.close()
    proc.wait()
    print(f"Video rendered successfully to {output_mp4} ({round(total_duration, 1)}s)")

if __name__ == "__main__":
    main()
