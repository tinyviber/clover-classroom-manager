import { useEffect, type PropsWithChildren } from "react";
import { Toaster } from "@/components/ui/sonner";
import { useLabStore } from "@/store";

export function AppProviders({ children }: PropsWithChildren) {
  const theme = useLabStore((state) => state.theme);
  const setResolvedTheme = useLabStore((state) => state.setResolvedTheme);

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const applyTheme = () => {
      const resolvedTheme = theme === "system" ? (media.matches ? "dark" : "light") : theme;
      document.documentElement.classList.toggle("dark", resolvedTheme === "dark");
      document.documentElement.style.colorScheme = resolvedTheme;
      setResolvedTheme(resolvedTheme);
    };

    applyTheme();
    media.addEventListener("change", applyTheme);
    return () => media.removeEventListener("change", applyTheme);
  }, [setResolvedTheme, theme]);

  return (
    <>
      {children}
      <Toaster position="bottom-right" richColors closeButton />
    </>
  );
}
