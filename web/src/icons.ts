// Which tile an app gets: its bundled Simple Icons SVG, the favicon the router
// fetched for it, or a letter. Every image comes from the console itself
// (/icons/...), never from the internet, and an app the manifest does not
// know is a letter tile rather than a guessed URL. Kept apart from Preact so
// node can test it.

/** tools/iconpack's manifest.json: kind is "svg", "fav" or "letter". */
export type IconManifest = Record<string, { kind: string; color: string; name?: string }>;

/** The tile behind an app with no brand colour, and behind unknown and other. */
export const NEUTRAL = "#3A4658";

export interface Icon {
  /** svg: /icons/<id>.svg; img: the fetched favicon at /icons/<id>; letter: no image. */
  kind: "svg" | "img" | "letter";
  src?: string;
  /** The tile's colour, always #RRGGBB. */
  color: string;
  letter: string;
  /** A light tile: draw the glyph or letter dark, not white. */
  dark: boolean;
}

// The shape the backend accepts for an id (a dot allowed, so a dotted id
// keeps its icon), and never "..": an id becomes a path segment, so anything
// else never reaches a URL.
const ID = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const plainId = (id: string) => ID.test(id) && !id.includes("..");
const HEX = /^#[0-9A-Fa-f]{6}$/;

function luminance(hex: string): number {
  const c = [1, 3, 5].map((i) => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}

function letterOf(id: string, name: string): string {
  if (id === "unknown") return "?";
  if (id === "other") return "…";
  const s = (name || id).trim();
  return s ? s.charAt(0).toUpperCase() : "?";
}

/** The tile for an app id. A missing manifest means no icon pack: letters everywhere. */
export function appIcon(id: string, manifest: IconManifest | null | undefined, name = ""): Icon {
  const letter = letterOf(id, name);
  const e = manifest && plainId(id) && id !== "unknown" && id !== "other" ? manifest[id] : undefined;
  if (!e || !Object.prototype.hasOwnProperty.call(manifest, id)) return { kind: "letter", color: NEUTRAL, letter, dark: false };
  const color = HEX.test(e.color) ? e.color.toUpperCase() : NEUTRAL;
  const dark = luminance(color) > 0.5;
  if (e.kind === "svg") return { kind: "svg", src: `/icons/${id}.svg`, color, letter, dark };
  if (e.kind === "fav") return { kind: "img", src: `/icons/${id}`, color, letter, dark };
  return { kind: "letter", color, letter, dark };
}

/**
 * A chart layer's colour: the app's brand colour, unless it has none, is so
 * dark it would vanish on the page's dark ground, or is already used by
 * another layer; then the fallback. The colour chosen is added to `used`.
 */
export function layerColor(id: string, manifest: IconManifest | null | undefined, used: Set<string>, fallback: string): string {
  const i = appIcon(id, manifest);
  const brand = i.color !== NEUTRAL && luminance(i.color) >= 0.03 && !used.has(i.color) ? i.color : "";
  const c = brand || fallback;
  used.add(c);
  return c;
}
