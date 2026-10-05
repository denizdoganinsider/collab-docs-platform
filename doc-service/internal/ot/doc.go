// Package ot implements the plain-text operational transformation the README's
// "OT specification" section fixes: the operation format, Apply, Compose and
// Transform.
//
// Three invariants hold everywhere in this package:
//
//   - Positions and lengths are Unicode code points, never bytes or UTF-16
//     units. An emoji is one position.
//   - BaseLen(op) must equal the length of the document the op is applied to.
//     A mismatch is an error, never "apply what fits".
//   - Compose and Transform take canonical operations (see Normalize) and
//     return canonical ones.
//
// Tie-break for concurrent inserts at the same position: Transform places
// a's insert first. The convention for the whole system is that the op
// already in the log wins the position, so both the server and the client
// always pass the logged op as a:
//
//	server: _, clientOp' = Transform(loggedOp, clientOp)
//	client: serverOp', outstanding' = Transform(serverOp, outstanding)
//
// If the two sides ever disagree on which argument is a, concurrent inserts
// at one position converge to different texts.
package ot
