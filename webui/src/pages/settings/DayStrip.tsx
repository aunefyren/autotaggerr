import { SettingsField, SettingsSection, SettingsView } from "../../types";
import { useSchedule } from "./fields";

export type Lane = {
  section: SettingsSection;
  field: SettingsField;
  /** The value in the box now — edited or stored. */
  value: string;
  dirty: boolean;
  /** The job's switch is off: drawn hollow, because it will not run. */
  off: boolean;
};

const TICKS = [0, 3, 6, 9, 12, 15, 18, 21, 24];

function scrollToSection(id: string) {
  const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  document.getElementById(`section-${id}`)?.scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "start" });
}
const pct = (minute: number) => `${(minute / 1440) * 100}%`;

/**
 * Every background job on one 24-hour clock — the server's, since that is the one the
 * scheduler reads. A cron expression answers "when does this run"; only a picture of all
 * of them answers "what else runs then", which is what someone moving a schedule needs
 * to know. Weekly runs are placed at their time of day and tagged with the day, so the
 * Sunday scan is on the strip rather than off the edge of today.
 *
 * Edits redraw it before they are saved: a lane reads its schedule the way its field
 * does (the preview for an edited value), so moving the scan to 03:00 visibly lands it on
 * the metadata refresh.
 */
export function DayStrip({ lanes, clock }: { lanes: Lane[]; clock: SettingsView["clock"] }) {
  return (
    <section className="card daystrip" aria-label="When each job runs">
      <div className="daystrip-head">
        <h2 className="section-title">A day on the server</h2>
        <span className="dim" style={{ fontSize: 12 }}>
          {clock.zone} · now <span className="mono">{clock.label}</span>
        </span>
      </div>
      <div className="daystrip-body">
        <div className="daystrip-labels">
          {lanes.map((lane) => (
            <button
              key={lane.field.key}
              type="button"
              className={`daystrip-label${lane.off ? " is-off" : ""}`}
              onClick={() => scrollToSection(lane.section.id)}
              title={`Go to ${lane.section.title}`}
            >
              {lane.section.title}
              {lane.off && <span className="daystrip-off">off</span>}
            </button>
          ))}
          <span className="daystrip-axis-spacer" />
        </div>
        <div className="daystrip-lanes">
          {lanes.map((lane) => (
            <LaneTrack key={lane.field.key} lane={lane} />
          ))}
          <div className="daystrip-axis" aria-hidden="true">
            {TICKS.map((h) => (
              <span key={h} style={{ left: pct(h * 60) }}>
                {String(h).padStart(2, "0")}
              </span>
            ))}
          </div>
          <div className="daystrip-now" style={{ left: pct(clock.minute) }} aria-hidden="true" />
        </div>
      </div>
    </section>
  );
}

function LaneTrack({ lane }: { lane: Lane }) {
  const schedule = useSchedule(lane.field, lane.value, lane.dirty);
  // Few enough points to label each; past that the tags would run into one another,
  // and the time is in each mark's title instead.
  const tagged = (schedule?.day.length ?? 0) <= 6;

  return (
    <div className={`daystrip-lane${lane.off ? " is-off" : ""}${lane.dirty ? " is-dirty" : ""}`}>
      {!schedule ? null : schedule.error ? (
        <span className="daystrip-note">not a valid schedule</span>
      ) : schedule.dense ? (
        <div className="daystrip-band" title={schedule.summary}>
          <span>{schedule.summary?.toLowerCase() ?? "very often"}</span>
        </div>
      ) : (
        schedule.day.map((t) => (
          <span
            key={t.minute}
            className="daystrip-mark"
            style={{ left: pct(t.minute) }}
            title={`${t.label}${t.days ? ` · ${t.days}` : " · every day"}`}
          >
            {tagged && (
              // Late runs label to the left, or the tag would run past the strip's end.
              <span className={`daystrip-tag mono${t.minute > 18 * 60 ? " tag-left" : ""}`}>
                {t.label}
                {t.days && <span className="dim"> {t.days}</span>}
              </span>
            )}
          </span>
        ))
      )}
    </div>
  );
}
