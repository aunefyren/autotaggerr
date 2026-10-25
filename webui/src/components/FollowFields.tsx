/**
 * What following an artist wants: the release types, whether live albums and the like
 * count, and the year it starts from. One set of controls for the four places a follow
 * is shaped — an artist's page, Add artist, Follow artists and the default for new
 * artists — so the same words mean the same thing in each.
 */

export interface FollowShape {
  follow_types: string;
  follow_secondary: boolean;
  follow_from_year: number;
}

export const DEFAULT_FOLLOW: FollowShape = { follow_types: "Album,EP", follow_secondary: false, follow_from_year: 0 };

const FOLLOW_TYPES = ["Album", "EP", "Single", "Live", "Compilation", "Other"];

/** "Album, EP", or the default when none are set. */
export function followTypes(shape: Pick<FollowShape, "follow_types">): string[] {
  return (shape.follow_types || DEFAULT_FOLLOW.follow_types).split(",").map((t) => t.trim()).filter(Boolean);
}

/** One sentence saying what a follow of this shape wants. */
export function describeFollow(shape: FollowShape): string {
  const cutoff = shape.follow_from_year || 0;
  return `${followTypes(shape).join(", ") || "nothing"}${shape.follow_secondary ? ", including live albums, compilations and remixes" : ""}${cutoff > 0 ? `, released in ${cutoff} or later` : ""}`;
}

export function FollowFields({
  value,
  onChange,
  disabled,
  showYear = true,
  idPrefix,
}: {
  value: FollowShape;
  onChange: (patch: Partial<FollowShape>) => void;
  disabled?: boolean;
  /** The default for new artists picks its cutoff by its own choice, not a year. */
  showYear?: boolean;
  idPrefix: string;
}) {
  const types = followTypes(value);
  const cutoff = value.follow_from_year || 0;
  const toggleType = (t: string) => {
    const next = types.includes(t) ? types.filter((x) => x !== t) : [...types, t];
    onChange({ follow_types: next.join(",") });
  };

  return (
    <div className="stack" style={{ gap: 10 }}>
      <div className="row" style={{ gap: 6, flexWrap: "wrap" }} role="group" aria-label="Release types">
        {FOLLOW_TYPES.map((t) => (
          <button
            key={t}
            type="button"
            className={types.includes(t) ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
            aria-pressed={types.includes(t)}
            disabled={disabled}
            onClick={() => toggleType(t)}
          >
            {t}
          </button>
        ))}
      </div>
      <div>
        <label className="row" style={{ gap: 8, cursor: disabled ? "default" : "pointer" }}>
          <input
            type="checkbox"
            checked={value.follow_secondary}
            disabled={disabled}
            onChange={(e) => onChange({ follow_secondary: e.target.checked })}
          />
          <span style={{ fontSize: 12 }}>Include live albums, compilations and remixes</span>
        </label>
        <div className="dim" style={{ fontSize: 11, marginTop: 4 }}>
          Off by default: a full discography is mostly reissues, which buries what you actually lack.
        </div>
      </div>
      {showYear && (
        <div className="field">
          <label className="flabel" htmlFor={`${idPrefix}-from-year`}>Only from year</label>
          <div className="row" style={{ gap: 8 }}>
            <input
              id={`${idPrefix}-from-year`}
              className="input"
              type="number"
              min={1900}
              max={2200}
              style={{ maxWidth: 120 }}
              value={cutoff || ""}
              disabled={disabled}
              placeholder="Any"
              onChange={(e) => {
                const next = e.target.value === "" ? 0 : Number(e.target.value);
                if (!Number.isNaN(next)) onChange({ follow_from_year: next });
              }}
            />
            {cutoff > 0 && (
              <button type="button" className="btn btn-ghost btn-sm" disabled={disabled} onClick={() => onChange({ follow_from_year: 0 })}>
                Clear
              </button>
            )}
            <button type="button" className="btn btn-secondary btn-sm" disabled={disabled} onClick={() => onChange({ follow_from_year: new Date().getFullYear() })}>
              From now on
            </button>
          </div>
          <div className="dim" style={{ fontSize: 11, marginTop: 4 }}>
            {cutoff > 0
              ? `Albums first released before ${cutoff} are not wanted. Releases MusicBrainz has no date for are left out too — it cannot show they clear the cutoff.`
              : "Blank wants the whole back catalogue. Set a year to be told only about what came after it."}
          </div>
        </div>
      )}
    </div>
  );
}
