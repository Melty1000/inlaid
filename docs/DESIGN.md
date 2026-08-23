---
name: Inlaid
description: A precise terminal-native camera and capture instrument.
colors:
  canvas: "the user's terminal background"
  ink: "the user's terminal foreground"
  line: "adaptive #C7C3C7 / #5C5C5C"
  muted: "adaptive #4D4D4D / #A49FA5"
  faint: "adaptive #8E8E8E / #626262"
  signal-pink: "adaptive #AD3E76 / #F25D94"
  focus-violet: "adaptive #634BD0 / #874BFD"
  success: "adaptive #047A50 / #73F59F"
  record: "adaptive #C40046 / #FF5F87"
  warning: "adaptive #755E00 / #FDFF8C"
typography:
  interface:
    fontFamily: "the active terminal monospace font"
    fontWeight: 400
    lineHeight: 1
  emphasis:
    fontFamily: "the active terminal monospace font"
    fontWeight: 700
    lineHeight: 1
rounded:
  transport: "single-cell rounded box-drawing border"
spacing:
  tight: "1 cell"
  standard: "2 cells"
  section: "3 rows"
components:
  transport-button:
    backgroundColor: "transparent"
    textColor: "{colors.ink}"
    borderColor: "{colors.line}"
    typography: "{typography.emphasis}"
    rounded: "{rounded.transport}"
    padding: "1 row 2 cells"
  selected-segment:
    backgroundColor: "transparent"
    textColor: "{colors.signal-pink}"
    typography: "{typography.emphasis}"
    rounded: "{rounded.none}"
    padding: "0 rows 1 cell"
---

# Design System: Inlaid

## Overview

**Creative North Star: "The Modular Video Instrument"**

Inlaid behaves like a compact piece of video hardware translated into terminal language. The camera image is the content; controls form two precise inline channels beneath it, and the transport anchors the task. The application belongs to the user's terminal instead of painting a second dark application shell over it.

The system is restrained and operational: transparent surfaces, one pink interaction signal, one violet focus signal, and semantic recording or warning colors. It adapts foreground signals to light or dark terminal backgrounds and rejects fake desktop chrome, gradients, ornamental cards, and implementation diagrams on the operating surface.

**Key Characteristics:**

- Camera-first composition with controls always on the same page.
- Terminal-native geometry, permanently legible transport borders, and visible keyboard focus.
- Plain outcome language; diagnostics are disclosed only on request.
- Color is state, never decoration, and every colored state also uses words or marks.

## Colors

The palette is a near-black instrument panel with cool signal colors and high-contrast chalk-white text.

### Primary

- **Signal Pink:** Selected segments, hover feedback, and the pressed interaction pulse.

### Secondary

- **Focus Violet:** Keyboard focus, demo identity, and the Save channel.

### Tertiary

- **Record Red:** Record actions and armed state only.
- **Success Green:** Healthy live state only.
- **Warning Amber:** Paused, constrained, or recoverable states.

### Neutral

- **Terminal Canvas / Ink:** Inherited from the active Windows Terminal profile.
- **Adaptive Line:** Quiet button, preview, and section boundaries.
- **Adaptive Muted / Faint:** Secondary labels, dividers, and disabled hierarchy.

**The Signal Rarity Rule.** Pink and violet identify interaction and focus; they never become ambient glow or background decoration.

## Typography

**Display Font:** The user's active terminal monospace font  
**Body Font:** The user's active terminal monospace font  
**Label Font:** The user's active terminal monospace font

**Character:** The terminal owns the typeface and size. Hierarchy comes from weight, color, case, and placement so the interface survives profile and zoom changes without assuming a bundled font.

### Hierarchy

- **Title** (bold): Product identity and blocking-state headings.
- **Body** (regular): Explanations, current values, and recovery copy.
- **Label** (regular or bold, uppercase): Short channel names, modes, and transport actions.

**The No Microcopy Rule.** If text becomes illegible when the terminal is zoomed out, remove it or move it into Details instead of shrinking it further.

## Layout

The normal surface requires at least 80 columns by 24 rows. Above that threshold, the preview receives all flexible height. Camera controls, Save controls, transport, and the footer keep stable row allocations. Below the threshold, the entire dashboard is replaced by one centered recovery message with the required and current window sizes.

At wide sizes, segmented choices remain fully visible. At compact supported sizes, finite selectors collapse to a single current value with previous/next affordances. Controls never silently disappear because a row overflowed.

## Elevation & Depth

There are no shadows or application-wide fills. Depth comes from the user's terminal canvas, thin dividers, stable borders, and short-lived interaction states. The preview border describes a real frame boundary; separators describe functional regions.

**The Native Canvas Rule.** The interface may color state, focus, and a brief press, but it never replaces the user's terminal background.

## Shapes

Geometry is cell-aligned. The preview uses one quiet rectangular boundary; transport actions use stable rounded box-drawing borders in every state. Inline controls remain borderless. Pills, soft cards, fake windows, and ornamental frames do not belong in this system.

## Components

### Transport Buttons

- **Shape:** Three terminal rows with always-visible rounded box-drawing borders.
- **Primary:** Record uses Record Red text; other actions use Chalk Ink.
- **Hover / Focus:** Border shifts to Signal Pink on hover and Focus Violet on keyboard focus without changing size.
- **Pressed:** Mouse-down or keyboard activation produces a roughly 110 ms heavy-border pulse; mouse release activates only over the same control.

### Segmented Controls

- **Style:** Neutral options sit directly on the terminal canvas. The current option is bracketed in Signal Pink with no background fill.
- **Compact state:** Show one current value with previous/next affordances rather than truncating the full choice set.

### Toggles and Status

- **Style:** Always pair color with an explicit word such as ON, OFF, PAUSED, or DEMO PREVIEW.
- **Focus:** Use the same violet focus marker as every other interactive control.

### Look Control

- **Detail:** One Soft / Balanced / Crisp choice controls the related block and edge treatment together.
- **Rule:** Do not expose a second overlapping detail control on the operating surface.

### Preview

- **Style:** Dominant, centered, and bounded by one thin line.
- **Framing:** Fill Window visibly crops to use the whole region; Show Whole Camera centers the complete image and permits blank bars.

## Do's and Don'ts

### Do:

- **Do** lead with the live image and keep capture actions visible on the same page.
- **Do** ask for outcomes such as Fill Window, Saved Size, and High Quality.
- **Do** put camera dimensions and performance explanations behind Details.
- **Do** preserve complete keyboard and mouse parity with visible focus.
- **Do** keep hover, focus, press, loading, recording, saving, and disabled states visibly distinct without moving controls.

### Don't:

- **Don't** expose buffers, sample grids, column caps, or pipeline diagrams as ordinary settings.
- **Don't** silently hide controls at narrow sizes; show a direct recovery state.
- **Don't** use gradients, glow, fake desktop chrome, or nested cards.
- **Don't** paint opaque control strips or a hard-coded application background over the terminal profile.
- **Don't** use color as the only indication of state.
