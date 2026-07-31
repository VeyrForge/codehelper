//! Greedy draft→verify + KV rewind (R13 PLD seam).
//!
//! Pure verify is independent of the drafter. Rejected draft suffixes that were
//! already appended are undone via [`KvStore::truncate`].

use crate::kv::KvStore;

/// Outcome of greedy draft verification against target argmaxes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct VerifyResult {
    /// How many draft tokens matched the target (prefix).
    pub accepted: usize,
    /// Accepted draft tokens (length = `accepted`).
    pub accepted_tokens: Vec<u32>,
    /// On reject (or after full accept): target's next token at the verify position.
    pub bonus_token: Option<u32>,
}

/// Greedy speculative acceptance (Leviathan / Chen).
///
/// `target_argmax[i]` = target's predicted next token after consuming
/// `prefix ++ draft[0..i]`. Length must be `draft.len()` (predictions for each
/// draft position). Optionally pass one extra argmax for the bonus token after
/// a full accept (`target_argmax.len() == draft.len() + 1`).
pub fn verify_draft_greedy(draft: &[u32], target_argmax: &[u32]) -> VerifyResult {
    debug_assert!(
        target_argmax.len() == draft.len() || target_argmax.len() == draft.len() + 1,
        "target_argmax len {} vs draft {}",
        target_argmax.len(),
        draft.len()
    );
    let mut accepted_tokens = Vec::with_capacity(draft.len());
    for (i, &d) in draft.iter().enumerate() {
        let Some(&pred) = target_argmax.get(i) else {
            break;
        };
        if pred != d {
            return VerifyResult {
                accepted: accepted_tokens.len(),
                accepted_tokens,
                bonus_token: Some(pred),
            };
        }
        accepted_tokens.push(d);
    }
    let bonus = target_argmax.get(draft.len()).copied();
    VerifyResult {
        accepted: accepted_tokens.len(),
        accepted_tokens,
        bonus_token: bonus,
    }
}

/// Truncate KV to `seq_before + accepted` after a draft verify that overshot.
pub fn rewind_rejected_drafts(kv: &mut dyn KvStore, seq_before: usize, accepted: usize) {
    kv.truncate(seq_before.saturating_add(accepted));
}

/// Greedy verify then rewind rejected draft suffix already appended to `kv`.
pub fn verify_and_rewind(
    kv: &mut dyn KvStore,
    seq_before: usize,
    draft: &[u32],
    target_argmax: &[u32],
) -> VerifyResult {
    let vr = verify_draft_greedy(draft, target_argmax);
    rewind_rejected_drafts(kv, seq_before, vr.accepted);
    vr
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::kv::{KvStore, RamKvStore};

    fn append_n(kv: &mut RamKvStore, n: usize) {
        let dim = kv.dim();
        let zero = vec![0u16; dim];
        for _ in 0..n {
            let t = kv.seq_len();
            for layer in 0..kv.layers() {
                kv.append(layer, t, &zero, &zero);
            }
        }
    }

    #[test]
    fn verify_greedy_accepts_prefix() {
        let draft = [1u32, 2, 3];
        let target = [1u32, 2, 9, 7]; // reject at 2; bonus = 9
        let vr = verify_draft_greedy(&draft, &target);
        assert_eq!(vr.accepted, 2);
        assert_eq!(vr.accepted_tokens, vec![1, 2]);
        assert_eq!(vr.bonus_token, Some(9));
    }

    #[test]
    fn verify_greedy_full_accept_with_bonus() {
        let draft = [5u32, 6];
        let target = [5u32, 6, 42];
        let vr = verify_draft_greedy(&draft, &target);
        assert_eq!(vr.accepted, 2);
        assert_eq!(vr.bonus_token, Some(42));
    }

    #[test]
    fn verify_and_rewind_rejects_mismatch() {
        let mut kv = RamKvStore::new(2, 4);
        append_n(&mut kv, 3);
        let seq_before = kv.seq_len();
        assert_eq!(seq_before, 3);
        // Append 2 draft tokens into KV, then reject first.
        append_n(&mut kv, 2);
        assert_eq!(kv.seq_len(), 5);
        let draft = [10u32, 11];
        let target = [99u32, 11]; // mismatch at 0
        let vr = verify_and_rewind(&mut kv, seq_before, &draft, &target);
        assert_eq!(vr.accepted, 0);
        assert_eq!(vr.bonus_token, Some(99));
        assert_eq!(kv.seq_len(), 3);
    }

    #[test]
    fn verify_and_rewind_accept_all() {
        let mut kv = RamKvStore::new(1, 2);
        append_n(&mut kv, 1);
        let seq_before = kv.seq_len();
        append_n(&mut kv, 2);
        let draft = [1u32, 2];
        let target = [1u32, 2, 3];
        let vr = verify_and_rewind(&mut kv, seq_before, &draft, &target);
        assert_eq!(vr.accepted, 2);
        assert_eq!(vr.bonus_token, Some(3));
        assert_eq!(kv.seq_len(), 3);
    }
}
