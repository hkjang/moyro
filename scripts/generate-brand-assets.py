#!/usr/bin/env python3
"""Generate moyro raster icons from the canonical mark geometry.

The source of truth remains the SVG mark. This script mirrors its coordinates
for platforms that require PNG or ICO files and intentionally has no network
dependency. Pillow 10 or newer is required.
"""

from __future__ import annotations

from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont, ImageOps


ROOT = Path(__file__).resolve().parents[1]
WEB_BRAND = ROOT / "webapp" / "public" / "brand"
DOCS_BRAND = ROOT / "docs" / "assets" / "brand"
BLUE = "#315FEA"
VIOLET = "#6849D8"
MINT = "#72F0C9"
INK = "#15203B"


def diagonal_gradient(size: int, start: str, end: str) -> Image.Image:
    vertical = Image.linear_gradient("L").resize((size, size), Image.Resampling.BICUBIC)
    horizontal = vertical.transpose(Image.Transpose.ROTATE_90)
    diagonal = ImageChops.add(vertical, horizontal, scale=2)
    return ImageOps.colorize(diagonal, start, end).convert("RGBA")


def cubic(start: tuple[float, float], control_a: tuple[float, float],
          control_b: tuple[float, float], end: tuple[float, float], steps: int = 24):
    for index in range(steps + 1):
        t = index / steps
        inverse = 1 - t
        yield (
            inverse ** 3 * start[0] + 3 * inverse ** 2 * t * control_a[0]
            + 3 * inverse * t ** 2 * control_b[0] + t ** 3 * end[0],
            inverse ** 3 * start[1] + 3 * inverse ** 2 * t * control_a[1]
            + 3 * inverse * t ** 2 * control_b[1] + t ** 3 * end[1],
        )


def scaled_points(points, scale: float):
    return [(round(x * scale), round(y * scale)) for x, y in points]


def draw_rounded_line(draw: ImageDraw.ImageDraw, points, width: int, fill):
    draw.line(points, fill=fill, width=width, joint="curve")
    radius = width / 2
    for x, y in (points[0], points[-1]):
        draw.ellipse((x - radius, y - radius, x + radius, y + radius), fill=fill)


def mark(size: int = 2048, *, maskable: bool = False) -> Image.Image:
    scale = size / 64
    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    gradient = diagonal_gradient(size, BLUE, VIOLET)
    mask = Image.new("L", (size, size), 0)
    mask_draw = ImageDraw.Draw(mask)
    if maskable:
        mask_draw.rectangle((0, 0, size, size), fill=255)
    else:
        mask_draw.rounded_rectangle((0, 0, size - 1, size - 1), radius=round(16 * scale), fill=255)
    canvas.alpha_composite(Image.composite(gradient, Image.new("RGBA", canvas.size), mask))

    highlight_layer = Image.new("RGBA", canvas.size, (0, 0, 0, 0))
    highlight_draw = ImageDraw.Draw(highlight_layer)
    highlight = list(cubic((12, 4.5), (26, -1), (48, 3), (59.5, 17)))
    draw_rounded_line(
        highlight_draw,
        scaled_points(highlight, scale),
        round(7 * scale),
        (255, 255, 255, 33),
    )
    canvas.alpha_composite(highlight_layer)

    draw = ImageDraw.Draw(canvas)

    flow = [(16, 44), (16, 26)]
    flow.extend(cubic((16, 26), (16, 20.4), (19, 17), (23.2, 17)))
    flow.extend(cubic((23.2, 17), (26.2, 17), (28.3, 18.7), (30, 21.2)))
    flow.extend([(32, 24.2), (34, 21.2)])
    flow.extend(cubic((34, 21.2), (35.7, 18.7), (37.8, 17), (40.8, 17)))
    flow.extend(cubic((40.8, 17), (45, 17), (48, 20.4), (48, 26)))
    flow.append((48, 44))
    draw_rounded_line(draw, scaled_points(flow, scale), round(7.5 * scale), (255, 255, 255, 255))

    cx, cy, radius = 32 * scale, 24.2 * scale, 2.8 * scale
    draw.ellipse((cx - radius, cy - radius, cx + radius, cy + radius), fill=MINT)
    return canvas


def save_icons(directory: Path):
    directory.mkdir(parents=True, exist_ok=True)
    standard = mark()
    maskable = mark(maskable=True)
    for size, name in (
        (16, "favicon-16.png"),
        (32, "favicon-32.png"),
        (180, "apple-touch-icon.png"),
        (192, "icon-192.png"),
        (512, "icon-512.png"),
    ):
        source = maskable if name == "apple-touch-icon.png" else standard
        source.resize((size, size), Image.Resampling.LANCZOS).save(
            directory / name, format="PNG", optimize=True
        )
    maskable.resize((512, 512), Image.Resampling.LANCZOS).save(
        directory / "maskable-icon-512.png", format="PNG", optimize=True
    )

    ico_frames = [standard.resize((size, size), Image.Resampling.LANCZOS) for size in (16, 32, 48)]
    ico_frames[-1].save(
        directory / "favicon.ico",
        format="ICO",
        append_images=ico_frames[:-1],
        sizes=[(16, 16), (32, 32), (48, 48)],
    )


def first_font(paths: list[str], size: int) -> ImageFont.FreeTypeFont:
    for candidate in paths:
        if Path(candidate).exists():
            return ImageFont.truetype(candidate, size=size)
    return ImageFont.load_default(size=size)


def social_card(path: Path):
    width, height = 1200, 630
    card = diagonal_gradient(width, "#0C1735", "#293F84").crop((0, 0, width, height))
    draw = ImageDraw.Draw(card, "RGBA")

    # Quiet connection graph: it adds the collaboration motif without
    # competing with the compact, high-contrast mark and headline.
    nodes = [(775, 118), (1005, 195), (835, 335), (1060, 455), (710, 522)]
    links = [(0, 1), (0, 2), (1, 2), (1, 3), (2, 3), (2, 4), (3, 4)]
    for start, end in links:
        draw.line((nodes[start], nodes[end]), fill=(160, 179, 255, 34), width=2)
    for x, y in nodes:
        draw.ellipse((x - 7, y - 7, x + 7, y + 7), fill=(114, 240, 201, 120))

    logo = mark(720).resize((176, 176), Image.Resampling.LANCZOS)
    glow = Image.new("RGBA", card.size, (0, 0, 0, 0))
    glow.alpha_composite(logo, (92, 108))
    glow = glow.filter(ImageFilter.GaussianBlur(28))
    glow.putalpha(glow.getchannel("A").point(lambda alpha: alpha // 4))
    card.alpha_composite(glow)
    card.alpha_composite(logo, (92, 108))

    wordmark_font = first_font([
        "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
        "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
    ], 112)
    korean_font = first_font([
        str(Path.home() / ".fonts" / "NanumSquareNeo-bRg.ttf"),
        str(Path.home() / ".fonts" / "NanumGothic.ttf"),
        "/usr/share/fonts/opentype/ipafont-gothic/ipag.ttf",
        "/usr/share/fonts/opentype/unifont/unifont_jp.otf",
    ], 42)
    label_font = first_font([
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
        "/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
    ], 27)
    draw.text((305, 126), "moyro", font=wordmark_font, fill=(255, 255, 255, 255),
              stroke_width=0)
    draw.text((96, 332), "팀의 대화와 업무 흐름을 조직 안에.", font=korean_font,
              fill=(225, 232, 255, 255))
    draw.rounded_rectangle((96, 420, 526, 478), radius=29, fill=(255, 255, 255, 20),
                           outline=(255, 255, 255, 42), width=1)
    draw.text((124, 433), "OFFLINE · OIDC · AI · MCP", font=label_font,
              fill=(180, 197, 255, 255))
    draw.text((98, 548), "hkjang.github.io/moyro", font=label_font,
              fill=(164, 177, 212, 255))
    card.convert("RGB").save(path, format="PNG", optimize=True)


def main():
    for destination in (WEB_BRAND, DOCS_BRAND):
        save_icons(destination)
    social_card(DOCS_BRAND / "moyro-social-card.png")
    (ROOT / "webapp" / "public" / "favicon.ico").write_bytes(
        (WEB_BRAND / "favicon.ico").read_bytes()
    )
    (ROOT / "docs" / "favicon.ico").write_bytes((DOCS_BRAND / "favicon.ico").read_bytes())
    (ROOT / "docs" / "apple-touch-icon.png").write_bytes(
        (DOCS_BRAND / "apple-touch-icon.png").read_bytes()
    )


if __name__ == "__main__":
    main()
