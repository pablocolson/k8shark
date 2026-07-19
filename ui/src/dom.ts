// isTypingTarget reports whether a keyboard event's target is somewhere text
// entry is expected (an input/textarea/contenteditable), so global keyboard
// shortcuts (App.tsx's "/"/space/Escape, TrafficTable's ArrowUp/ArrowDown row
// navigation) know to stay out of the way.
export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  return !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable);
}
