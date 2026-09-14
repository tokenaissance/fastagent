-- Read-only audit: how often does the agent answer a Chinese message in English?
--
-- Why this exists (docs/prompt-inventory.md, finding 3): the corpus is English
-- almost everywhere — the system prompt modules, and the per-turn blocks that are
-- re-sent on EVERY turn (renderSender, renderClientParams, channel hints,
-- failed-rounds, loop-detected …). The only place a default language is stated is
-- the agent's SOUL.md, which is at the top of the prompt; renderSender's own
-- comment is the observation that started this: "SOUL.md's 默认中文 loses to N
-- copies of 'The latest user turn was sent by:…' surrounding it".
--
-- Before rewriting any wording, measure it:
--
--   psql "$FASTAGENT_STORAGE_DSN" -f scripts/prompt-language-audit.sql
--
-- Reading the result: `zh_in_en_out_pct` is the share of REPLIES that came back
-- English after a Chinese question, per channel. Compare channels (the English
-- per-turn blocks only fire on IM) and agents rather than reading one number in
-- isolation — the classifier is a ratio over non-space characters, so code blocks,
-- paths and English product names pull any message towards "English".

WITH msgs AS (
    SELECT
        s.agent_id,
        s.session_key,
        COALESCE(NULLIF(s.channel, ''), 'unknown') AS channel,
        m.ord,
        m.msg ->> 'role' AS role,
        COALESCE(m.msg ->> 'content', '') AS content,
        -- CJK ratio: characters in the CJK Unified Ideographs block over all
        -- non-space characters. Approximate on purpose, and only ever compared.
        (
            LENGTH(COALESCE(m.msg ->> 'content', ''))
            - LENGTH(REGEXP_REPLACE(COALESCE(m.msg ->> 'content', ''), '[一-龥]', '', 'g'))
        )::numeric
        / NULLIF(
            LENGTH(REGEXP_REPLACE(COALESCE(m.msg ->> 'content', ''), '\s', '', 'g')),
            0
        ) AS cjk_ratio
    FROM sessions s
    CROSS JOIN LATERAL jsonb_array_elements(s.messages::jsonb) WITH ORDINALITY AS m(msg, ord)
    WHERE s.updated_at > now() - interval '30 days'
),
questions AS (
    SELECT agent_id, session_key, ord, cjk_ratio
    FROM msgs
    WHERE role = 'user' AND LENGTH(BTRIM(content)) > 0
),
replies AS (
    SELECT
        a.agent_id,
        a.session_key,
        a.channel,
        a.cjk_ratio,
        q.cjk_ratio AS question_cjk_ratio
    FROM msgs a
    JOIN LATERAL (
        SELECT cjk_ratio
        FROM questions q
        WHERE q.agent_id = a.agent_id
          AND q.session_key = a.session_key
          AND q.ord < a.ord
        ORDER BY q.ord DESC
        LIMIT 1
    ) q ON TRUE
    WHERE a.role = 'assistant' AND LENGTH(BTRIM(a.content)) > 0
),
scored AS (
    SELECT
        channel,
        question_cjk_ratio >= 0.30 AS asked_in_chinese,
        cjk_ratio >= 0.10 AS answered_in_chinese
    FROM replies
)
SELECT
    COALESCE(channel, '(all channels)') AS channel,
    COUNT(*) AS replies,
    COUNT(*) FILTER (WHERE asked_in_chinese) AS chinese_questions,
    COUNT(*) FILTER (WHERE asked_in_chinese AND NOT answered_in_chinese) AS answered_in_english,
    ROUND(
        100.0 * COUNT(*) FILTER (WHERE asked_in_chinese AND NOT answered_in_chinese)
        / NULLIF(COUNT(*) FILTER (WHERE asked_in_chinese), 0),
        1
    ) AS zh_in_en_out_pct
FROM scored
GROUP BY GROUPING SETS ((channel), ())
ORDER BY chinese_questions DESC NULLS LAST;
