/** A generated stand-in for a catalogue entry with no usable icon. */
export interface Monogram {
  /** One or two characters. */
  text: string;
  /** `#rrggbb`. */
  background: string;
  /** `#000000` or `#ffffff`, whichever contrasts more with the background. */
  ink: string;
}

/** FNV-1a, for a colour that is the same on every render and every machine. */
function hash(value: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < value.length; i++) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/** Up to two letters. A digit ends a word, so "Context7" is C7 and not Co. */
export function initials(label: string): string {
  const two = (words: string[]) => (words[0]![0]! + words[1]![0]!).toUpperCase();

  const words = label.replace(/[._/\\-]+/g, " ").split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  // Two words the reader can see beats a split inside the first one, so
  // "GitHub Issues" is GI rather than GH.
  if (words.length > 1) return two(words);

  const parts = words[0]!.replace(/([a-z])([A-Z0-9])/g, "$1 $2").split(" ");
  if (parts.length > 1) return two(parts);
  return words[0]!.slice(0, 2).toUpperCase();
}

function hsl(h: number, s: number, l: number): string {
  const a = s * Math.min(l, 1 - l);
  const channel = (n: number) => {
    const k = (n + h / 30) % 12;
    const v = l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
    return Math.round(v * 255);
  };
  return "#" + [channel(0), channel(8), channel(4)]
    .map((c) => c.toString(16).padStart(2, "0")).join("");
}

const srgb = (c: number) =>
  c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;

export function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  return 0.2126 * srgb(r!) + 0.7152 * srgb(g!) + 0.0722 * srgb(b!);
}

/**
 * Black or white, whichever is further from the background. The two ratios
 * cross at 4.58:1, so the winner always clears 4.5; a fixed ink would not.
 */
export function ink(background: string): string {
  const l = luminance(background);
  return (l + 0.05) / 0.05 >= 1.05 / (l + 0.05) ? "#000000" : "#ffffff";
}

/**
 * The mark for one entry. Colour from `name`, the stable identifier, so a
 * retitled server keeps its colour; letters from the title, which is read.
 */
export function monogram(name: string, title?: string): Monogram {
  const label = title?.trim() || name.split("/").pop() || name;
  const background = hsl(hash(name) % 360, 0.55, 0.62);
  return { text: initials(label), background, ink: ink(background) };
}
