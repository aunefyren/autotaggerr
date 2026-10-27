import { StorageVolume } from "../types";

/** A size in binary units with the familiar labels — the same arithmetic the server's messages use. */
export function formatBytes(bytes: number): string {
  if (!bytes || bytes < 1024) return `${bytes || 0} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let value = bytes / 1024;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return `${value >= 100 ? value.toFixed(0) : value.toFixed(1)} ${units[i]}`;
}

/**
 * One disk: what Autotaggerr put on it against what is left. Music, Autotaggerr's own
 * data, everything else on the disk, and free space, in that order along one bar.
 *
 * Autotaggerr's data is megabytes on a disk of terabytes, and a small library can be
 * too, so at true scale either can be no pixels at all; each is drawn at least 3px
 * wide when there is any, and its figure is in the legend beside it. A sliver that is
 * slightly too wide is honest about there being something; a segment that is not drawn
 * says there is nothing.
 *
 * Free space under a tenth of the disk is told in the warning colour, under a
 * twentieth in the danger colour — the question this bar exists to answer is whether
 * a disk is running out.
 */
export function DiskRow({ volume, label }: { volume: StorageVolume; label?: string }) {
  const name = volume.path || "Autotaggerr's data";
  if (volume.error) {
    return (
      <div className="disk">
        <div className="disk-head">
          <span className="mono disk-name">{name}</span>
          <span className="help-err">Could not be measured: {volume.error}</span>
        </div>
      </div>
    );
  }
  const total = volume.total || 1;
  const used = Math.max(0, volume.total - volume.free);
  const other = Math.max(0, used - volume.music - volume.app);
  const pct = (n: number) => `${(n / total) * 100}%`;
  const share = volume.free / total;
  const freeClass = share < 0.05 ? "is-danger" : share < 0.1 ? "is-warn" : "";

  return (
    <div className="disk">
      <div className="disk-head">
        <span className="mono disk-name" title={name}>
          {name}
        </span>
        {label && <span className="dim disk-holds">{label}</span>}
        <span className={`disk-free ${freeClass}`}>
          {formatBytes(volume.free)} free of {formatBytes(volume.total)}
        </span>
      </div>
      <div
        className="disk-bar"
        role="img"
        aria-label={`${name}: ${formatBytes(volume.music)} music, ${formatBytes(volume.app)} Autotaggerr, ${formatBytes(other)} other files, ${formatBytes(volume.free)} free`}
      >
        {volume.music > 0 && <span className="disk-seg music" style={{ width: pct(volume.music) }} />}
        {volume.app > 0 && <span className="disk-seg app" style={{ width: pct(volume.app) }} />}
        {other > 0 && <span className="disk-seg other" style={{ width: pct(other) }} />}
      </div>
      <div className="disk-legend">
        {volume.music > 0 && (
          <span>
            <i className="disk-key music" /> Music {formatBytes(volume.music)}
          </span>
        )}
        {volume.holds_app && (
          <span>
            <i className="disk-key app" /> Autotaggerr {formatBytes(volume.app)}
          </span>
        )}
        <span>
          <i className="disk-key other" /> Other files {formatBytes(other)}
        </span>
      </div>
    </div>
  );
}

/** What a disk holds, for the line above its bar: its libraries, and config/ when it is there. */
export function diskHolds(volume: StorageVolume): string {
  const parts = [...volume.libraries];
  if (volume.holds_app && volume.libraries.length > 0) parts.push("Autotaggerr's data");
  return parts.join(", ");
}
