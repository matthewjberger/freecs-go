package freecs

// Parallel iteration dispatches one goroutine per matching archetype. Each
// goroutine materializes typed []T slices from its archetype's columns and
// runs callback over every row. This is a good fit when many archetypes
// match the query and the per-entity work is non-trivial. It is *not*
// helpful when a single archetype holds most of the entities; the goroutine
// fan-out is per archetype, not per row.
//
// The callback must not mutate world topology (no Spawn, Despawn,
// AddComponents, RemoveComponents, Set on a missing component, or tag
// inserts). Any of those would race with sibling goroutines walking other
// archetypes whose tableEdges, queryCache, or tag-set state could be
// touched. Use the command buffer (QueueDespawn, QueueSet, etc.) to defer
// structural changes safely.
//
// Reading and writing component values through the supplied pointers is
// safe: each archetype is the sole owner of its columns and no two
// goroutines touch the same column. Reading shared world.resources is fine;
// writing to them from inside the callback is the caller's responsibility
// to synchronize.
