import { useEffect, useRef } from "react";

/**
 * The sign-in panel's background: a field of systems, faintly linked, that
 * the cursor lights up as it passes.
 *
 * The picture is the thing mcpd is -- many systems, reached through one
 * place -- and the interaction is the only motion on the page that is not a
 * response to a control: the nodes drift, and the ones near the pointer lean
 * toward it and their links brighten, then settle when it leaves. It is kept
 * quiet on purpose. A field that demands attention on a sign-in page is
 * competing with the one thing the page is for.
 *
 * It is a canvas layered behind ordinary text, and it is allowed to be absent.
 * The heading and the facts are DOM, the panel has its colour from CSS, and
 * nothing on the sign-in path waits for this to draw. Where there is no 2D
 * context -- a test runner, a browser with canvas turned off -- the component
 * renders an empty element and does nothing else.
 *
 * Colours are read from the panel tokens at mount rather than written here,
 * so the field follows the palette like everything else does.
 */

/** Pixels of panel per node. The count follows the area, not the window. */
const AREA_PER_NODE = 16_000;
const MAX_NODES = 90;
/** Two nodes closer than this are drawn linked. */
const LINK_DISTANCE = 150;
/** How far from the pointer the field responds. */
const REACH = 240;

type Node = { x: number; y: number; vx: number; vy: number; r: number };

/** Parses `#rrggbb` into its channels, for `rgba(...)` with an alpha. */
function rgb(hex: string): string {
  const h = hex.trim().replace("#", "");
  if (h.length !== 6) return "136, 192, 208";
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)).join(", ");
}

/** 0 at the edge of the reach, 1 at the pointer, eased between. */
function reveal(distance: number): number {
  if (distance >= REACH) return 0;
  const t = 1 - distance / REACH;
  return t * t * (3 - 2 * t);
}

export function NetworkField() {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    const parent = canvas?.parentElement;
    if (!canvas || !parent) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const style = getComputedStyle(document.documentElement);
    const accent = rgb(style.getPropertyValue("--panel-accent"));
    const ink = rgb(style.getPropertyValue("--panel-foreground"));

    // Motion is the affordance being asked to stop, so under reduced motion
    // the nodes hold still. The pointer still lights what it is near: that is
    // a response to the person's own action, which is the kind of motion the
    // preference is not about.
    const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    let width = 0;
    let height = 0;
    let nodes: Node[] = [];
    let pointer: { x: number; y: number } | null = null;
    let frame = 0;
    let running = false;

    function seed() {
      const count = Math.min(MAX_NODES, Math.round((width * height) / AREA_PER_NODE));
      nodes = Array.from({ length: count }, () => ({
        x: Math.random() * width,
        y: Math.random() * height,
        vx: (Math.random() - 0.5) * 0.12,
        vy: (Math.random() - 0.5) * 0.12,
        r: 1.2 + Math.random() * 1.4,
      }));
    }

    function resize() {
      const rect = parent!.getBoundingClientRect();
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      width = rect.width;
      height = rect.height;
      canvas!.width = Math.round(width * dpr);
      canvas!.height = Math.round(height * dpr);
      canvas!.style.width = `${width}px`;
      canvas!.style.height = `${height}px`;
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0);
      // Reseeding on every resize would make the field jump as a window is
      // dragged; the nodes are kept and only the count is topped up or cut.
      if (nodes.length === 0) seed();
      else {
        const want = Math.min(MAX_NODES, Math.round((width * height) / AREA_PER_NODE));
        while (nodes.length < want) {
          nodes.push({
            x: Math.random() * width, y: Math.random() * height,
            vx: (Math.random() - 0.5) * 0.12, vy: (Math.random() - 0.5) * 0.12,
            r: 1.2 + Math.random() * 1.4,
          });
        }
        nodes.length = Math.min(nodes.length, want);
      }
      draw();
    }

    function step() {
      for (const n of nodes) {
        if (pointer) {
          // The lean toward the pointer is a nudge to velocity, not a move,
          // so a node arrives rather than snapping, and drifts off again
          // rather than being dropped.
          const dx = pointer.x - n.x;
          const dy = pointer.y - n.y;
          const d = Math.hypot(dx, dy);
          const pull = reveal(d) * 0.004;
          if (d > 1) {
            n.vx += (dx / d) * pull;
            n.vy += (dy / d) * pull;
          }
        }
        // Friction, so a nudge decays instead of accumulating into a swarm.
        n.vx *= 0.985;
        n.vy *= 0.985;
        n.x += n.vx;
        n.y += n.vy;
        // Wrap rather than bounce: a bounce is a visible event at the edge,
        // and this field is not supposed to have events.
        if (n.x < -10) n.x = width + 10;
        if (n.x > width + 10) n.x = -10;
        if (n.y < -10) n.y = height + 10;
        if (n.y > height + 10) n.y = -10;
      }
    }

    function draw() {
      ctx!.clearRect(0, 0, width, height);

      // Links first, under the nodes.
      for (let i = 0; i < nodes.length; i++) {
        const a = nodes[i]!;
        for (let j = i + 1; j < nodes.length; j++) {
          const b = nodes[j]!;
          const dx = a.x - b.x;
          const dy = a.y - b.y;
          const d = Math.hypot(dx, dy);
          if (d > LINK_DISTANCE) continue;
          // Faint by distance between the pair, brighter near the pointer.
          const near = pointer
            ? reveal(Math.hypot(pointer.x - (a.x + b.x) / 2, pointer.y - (a.y + b.y) / 2))
            : 0;
          const alpha = (1 - d / LINK_DISTANCE) * (0.07 + near * 0.45);
          ctx!.strokeStyle = `rgba(${accent}, ${alpha.toFixed(3)})`;
          ctx!.lineWidth = 1;
          ctx!.beginPath();
          ctx!.moveTo(a.x, a.y);
          ctx!.lineTo(b.x, b.y);
          ctx!.stroke();
        }
      }

      for (const n of nodes) {
        const near = pointer ? reveal(Math.hypot(pointer.x - n.x, pointer.y - n.y)) : 0;
        const alpha = 0.28 + near * 0.72;
        ctx!.fillStyle = near > 0.5
          ? `rgba(${accent}, ${alpha.toFixed(3)})`
          : `rgba(${ink}, ${alpha.toFixed(3)})`;
        ctx!.beginPath();
        ctx!.arc(n.x, n.y, n.r + near * 1.2, 0, Math.PI * 2);
        ctx!.fill();
      }
    }

    function loop() {
      if (!running) return;
      step();
      draw();
      frame = requestAnimationFrame(loop);
    }

    function start() {
      if (running || still) return;
      running = true;
      frame = requestAnimationFrame(loop);
    }

    function stop() {
      running = false;
      cancelAnimationFrame(frame);
    }

    // A hidden tab draws nothing: it would be work nobody sees, on a laptop
    // battery.
    function onVisibility() {
      if (document.hidden) stop();
      else start();
    }

    function onMove(e: PointerEvent) {
      const rect = parent!.getBoundingClientRect();
      pointer = { x: e.clientX - rect.left, y: e.clientY - rect.top };
      if (still) draw();
    }
    function onLeave() {
      pointer = null;
      if (still) draw();
    }

    resize();
    start();
    const observer = new ResizeObserver(resize);
    observer.observe(parent);
    parent.addEventListener("pointermove", onMove);
    parent.addEventListener("pointerleave", onLeave);
    document.addEventListener("visibilitychange", onVisibility);

    return () => {
      stop();
      observer.disconnect();
      parent.removeEventListener("pointermove", onMove);
      parent.removeEventListener("pointerleave", onLeave);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, []);

  return (
    <canvas
      ref={ref}
      aria-hidden="true"
      className="pointer-events-none absolute inset-0"
    />
  );
}
