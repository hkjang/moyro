---
name: emoticon-art
description: Draw KakaoTalk-style cartoon emoticon characters as code-rendered SVG (chibi proportions, ink outlines, cel shading, expression and pose systems) and verify them with a render-and-look loop. Use when adding or restyling emoticons, stickers, mascots, or character packs in webapp/src/features/workspace/stickers.
---

# Emoticon art (cartoon character SVG)

Emoticons in this product are drawn at render time from specs
(`stickers.ts`) by `Sticker.tsx`. The look is *messenger cartoon*: chibi
characters with heavy ink outlines, flat colour with one cel shadow and one
highlight, huge readable eyes, and a pose that acts out the caption.
Nothing here uses an external image service — the product ships offline.

## Workflow (always)

1. **Read the spec model** in `stickers.ts` (`eyes`, `mouth`, `brow`, `pose`,
   `prop`, `float`, `character`). Add fields only when a drawing need cannot be
   expressed with the existing ones.
2. **Draw in `Sticker.tsx`** following the rules below.
3. **Render and look.** Write the preview page with the vitest helper
   (`renderToStaticMarkup` over `STICKERS`), screenshot it with the bundled
   Playwright Chromium, and *view the image*. Never judge SVG by reading it.
4. **Fix what looks wrong, re-render.** Two passes minimum. Check: nothing
   overlaps the caption bubble; every character is recognisable at 52px;
   colours differ per character; no unintended identical shapes (gradient id
   collisions show as every character sharing one colour).
5. Run `stickers.test.ts`; keep ids stable (they are stored in sent posts).

## Proportions (chibi, 128×128 canvas)

- Two heads tall. Head centre (64, 50), radius ≈ 32. Body below, from y≈72
  to y≈104, narrower than the head (width ≈ 36–40). Feet at y≈110.
- The figure sits 6–8px above the caption bubble (`translate(0 -7)`).
- Face features live in the lower two-thirds of the head: eyes at y≈50,
  mouth at y≈68–74, cheeks at y≈60. Ears/tufts attach at the head's top edge.

## Line work

- One ink colour for every outline (`#22304B`). Outer silhouettes 3.6px,
  interior features 2.2–2.6px, fine details (whiskers, motion lines) 1.6–2px.
- Every filled body part gets an outline with round joins/caps. Limbs are
  drawn as a thick ink stroke with a thinner colour stroke on top (a "noodle"
  limb) so they read as outlined tubes without path maths.
- Motion lines, "!!", "??", sweat drops, anger marks are ink-coloured and
  sit outside the silhouette.

## Colour and shading

- Per character: base, shade (base darkened ~18%), highlight (white at 35–45%
  opacity), accent (inner ear, belly, muzzle), cheek pink `#F7A1B5`.
- **Flat fill plus one cel shadow and one highlight.** No radial "plastic"
  gradients: the shadow is a hard-edged shape along the lower-left of the head
  and body; the highlight is one soft ellipse upper-left on the head.
- Keep 4–5 hues per emoticon including the ink. Props are the only place a
  sixth colour may appear.

## Eyes and expression

- Default eye: white ellipse (rx 7–9) with ink outline 2.4, a coloured iris
  (per character: navy for most, brown for bear/dog), a pupil, and two
  highlights — one large upper-right, one small lower-left. Add a thicker
  upper lash line (ink, 3px) so eyes read as drawn, not stamped.
- Expression set: dot (calm), happy (arcs), closed (soft arcs), wide (shock),
  tear (with drops), wink, sleepy (flat + lid line), star (achievement).
- Always draw eyebrows; even neutral faces get faint brows. Angry: inward
  slant; sad: outward slant; surprised: raised.
- Mouths: smile, grin (with teeth and tongue), open (with tongue), frown,
  flat, wavy (embarrassed), o, tongue. Dogs add a hanging tongue on happy
  mouths; ducks replace the mouth with a beak that opens.

## Pose language

Each caption is acted out with the body, not only the face:
wave (greetings), thumbs (approval), cheer (both arms up, celebration),
hold (both hands in front holding the prop — food, coffee, memo), run
(leaning, legs apart, motion lines), slump (shoulders down, head tilt,
tired), hide (hands over the eyes, shy), cross (arms crossed, refusal),
cry (hands at the eyes), point (surprise/anger), sit. When adding an
emoticon, pick the pose first, then the face.

## Character identity

Each character has one silhouette feature that reads at 52px:
cat — triangular ears with pink inner, whiskers, forehead stripes, tail up
(happy) or down (sad); dog — long floppy ears, muzzle patch, hanging tongue,
wagging tail; bear — round ears with inner circles, muzzle, belly patch;
rabbit — long ears (droop when sad), pink inner ear, small nose; duck — beak,
head tuft, wing stubs; 모요 — smooth blob with a bright brand palette.

## Caption bubble

White comic bubble with ink outline at y 111–128, width from caption length
(≈10.5px per Korean character + 18), bold 10.5px ink text, small tail
pointing at the character. Captions must stay legible at 52px, so ≤ 7
characters is ideal, 10 is the hard limit.

## What not to do

- Do not paste emoji glyphs as the character or its face; glyphs are only
  acceptable as small hand-held props until vector props exist.
- Do not rely on `useId` for SVG `<defs>` ids — multiple React roots reset
  it and definitions collide. Derive ids from the spec id.
- Do not add a character without at least six emoticons in its own voice.
