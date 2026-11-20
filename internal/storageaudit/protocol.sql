-- {{path}} 已剥离查询串及末尾斜线；仅返回固定分类，资源 ID、供应商地址均不进入结果。
CASE
 WHEN {{path}} = '' THEN 'unrecorded'
 WHEN {{path}} LIKE '%/messages/count_tokens' THEN 'messages_count'
 WHEN {{path}} LIKE '%/chat/completions' THEN 'chat'
 WHEN {{path}} LIKE '%/responses' THEN 'responses'
 WHEN {{path}} LIKE '%/responses/%' THEN 'responses_resource'
 WHEN {{path}} LIKE '%/messages' THEN 'messages'
 WHEN {{path}} LIKE '%:generateContent' OR {{path}} LIKE '%:streamGenerateContent' THEN 'gemini'
 WHEN {{path}} LIKE '%/embeddings' THEN 'embeddings'
 WHEN {{path}} LIKE '%/images/%' THEN 'images'
 WHEN {{path}} LIKE '%/audio/%' THEN 'audio'
 WHEN {{path}} ~ '/videos(/|$)' THEN 'video'
 WHEN {{path}} ~ '/(files|uploads)(/|$)' THEN 'files'
 WHEN {{path}} LIKE '%/rerank' THEN 'rerank'
 WHEN {{path}} LIKE '%/moderations' THEN 'moderations'
 ELSE 'other' END
