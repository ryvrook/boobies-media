export function mountScrollTop(button: HTMLElement): void {
  let scheduled = false;

  function update(): void {
    scheduled = false;
    button.toggleAttribute("hidden", window.scrollY < 600);
  }

  window.addEventListener(
    "scroll",
    () => {
      if (scheduled) return;
      scheduled = true;
      window.requestAnimationFrame(update);
    },
    { passive: true },
  );
  button.addEventListener("click", () => {
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    window.scrollTo({ top: 0, behavior: reduced ? "auto" : "smooth" });
  });
  update();
}
