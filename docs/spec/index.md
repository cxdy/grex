# SPEC

The **SPEC** section holds grex design documents that describe product intent
and 1.0 scope. They sit beside (not above) the rest of this documentation site.

!!! warning "These documents change often"
    Design material is a **living** plan of record. Goals, non-goals, and
    milestones shift as implementation proceeds. Do not treat the design as a
    frozen API contract.

    For what the code does **today**, prefer:

    - [User](../user/index.md), [Admin](../admin/index.md), and
      [Developer](../developer/index.md) docs
    - [Observability](../observability/index.md) for metrics, logs, and traces
    - The repository source and tests

## Documents

| Document | Description |
|----------|-------------|
| [Design](design.md) | grex 1.0 design: architecture, features, milestones, and open decisions |

When design and implementation disagree, implementation wins until the design
is updated—or the design wins and the code still needs to catch up. File an
issue when you notice a durable mismatch.
