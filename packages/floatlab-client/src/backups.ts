import { parse } from "yaml";

// Restore uses the submitted configuration because the original owner may be offline.
export function backupRestoreDatasets(source: string): string[] {
  const document = parse(source) as {name?: string; services?: Record<string, {volumes?: (string | {type?: string; source?: string; read_only?: boolean})[]}>};
  if (!document?.name || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(document.name)) throw new Error("Compose must contain the original canonical stack name");
  const datasets = new Set<string>();
  for (const service of Object.values(document.services ?? {})) {
    for (const mount of service.volumes ?? []) {
      const parts = typeof mount === "string" ? mount.split(":") : undefined;
      const readOnly = parts ? parts[2]?.split(",").includes("ro") : typeof mount === "object" && mount.read_only;
      if (readOnly || typeof mount === "object" && mount.type === "tmpfs") continue;
      const path = parts && parts.length > 1 ? parts[0] : typeof mount === "object" ? mount.source : undefined;
      const base = path?.replace(/\/+$/, "").split("/").at(-1);
      if (!base || base === "." || base === "..") throw new Error("Compose contains an unsupported writable mount source");
      datasets.add(`floatlab/${document.name}/${base}`);
    }
  }
  if (!datasets.size) throw new Error("Compose must contain the backed-up writable mounts");
  return [...datasets];
}
