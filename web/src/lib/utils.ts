import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * Joins class names, letting a later one beat an earlier one.
 *
 * Plain concatenation does not: `"px-2 px-4"` leaves both in the class list and
 * the winner is whichever rule CSS emitted last, which a caller cannot see.
 * This is what makes `className` on a shadcn component an override rather than
 * a suggestion.
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
