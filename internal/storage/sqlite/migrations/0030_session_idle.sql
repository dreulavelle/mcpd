-- When a session was last used by a person.
--
-- Sessions expired by the clock alone: `expires_at` was set at sign-in and
-- nothing ever moved it, so somebody working steadily was signed out mid
-- sentence at the twelve-hour mark, and a browser left open on a signed-in
-- console stayed signed in until that same mark whether anybody was there or
-- not. Both are the wrong answer to "is this person still here".
--
-- Two clocks answer it properly, which is what the guidance for session
-- management has long recommended and what most consoles do: an idle timeout
-- that moves with the person, and an absolute one that does not move at all.
-- `expires_at` stays exactly what it was and becomes the second of those --
-- the ceiling nothing can push past. This column is the first.
--
-- It is deliberately not "when this session last made a request". The console
-- polls: the tunnels page every eight seconds, the overview every fifteen. A
-- timer reset by traffic would be reset by a page nobody is looking at, and
-- the idle timeout would never once fire. Only the browser can tell a click
-- from its own polling, so it says so, and this records what it said.
ALTER TABLE user_sessions ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0;

-- Sessions that predate this column have never reported activity. Seeding
-- them from when they were created rather than leaving a zero means an
-- upgrade does not sign out everybody who was signed in at the time -- they
-- keep whatever remains of their idle window, measured from sign-in.
UPDATE user_sessions SET last_seen_at = created_at WHERE last_seen_at = 0;
