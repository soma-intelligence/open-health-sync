/* biome-ignore-all lint/performance/noImgElement: a static export, the images are already sized */
import { asset } from "@/lib/asset";

/** A light and a dark capture of the same screen from docs/assets/readme. */
export function Shot({ name, alt }: { name: string; alt: string }) {
  return (
    <span className="not-prose my-6 block overflow-hidden rounded-xl border bg-fd-card shadow-sm">
      <img src={asset(`/assets/readme/${name}-light.png`)} alt={alt} className="block w-full dark:hidden" />
      <img src={asset(`/assets/readme/${name}-dark.png`)} alt={alt} className="hidden w-full dark:block" />
    </span>
  );
}
