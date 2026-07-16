import { useCallback, useState } from "react";

export type Theme = "light" | "dark";

const STORAGE_KEY = "k8shark.theme";

// index.html's inline script already resolved data-theme (localStorage, or
// the OS preference as a fallback) before this module ever runs, so reading
// it back here just mirrors that into React state — no flash, no guessing.
function currentTheme(): Theme {
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

export function useTheme(): { theme: Theme; toggleTheme: () => void } {
  const [theme, setTheme] = useState<Theme>(currentTheme);

  const toggleTheme = useCallback(() => {
    setTheme((prev) => {
      const next: Theme = prev === "dark" ? "light" : "dark";
      document.documentElement.dataset.theme = next;
      localStorage.setItem(STORAGE_KEY, next);
      return next;
    });
  }, []);

  return { theme, toggleTheme };
}
