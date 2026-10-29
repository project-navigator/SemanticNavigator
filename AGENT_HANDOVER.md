# Передача проекта — X2609-382 «Семантический навигатор», CodeBY

Сначала читайте README.md, DECISIONS.md и CONSULTATION_BRIEF.md. Исходный baseline `9442a0f`; изменения подготовлены локально, без push и публикации. Пользовательских изменений на старте не было.

## Устройство

- `extension/src/extension.ts`: активация, opt-in `enabled`, trust, watcher, debounce, последовательный перезапуск при изменениях конфигурации, команды и полная выдача в sidebar.
- `engineClient.ts`: запуск встроенного бинарника (или явно заданного enginePath), pending RPC, таймауты 25 секунд search / 10 секунд остальных RPC, отклонение ожиданий при остановке/выходе, остановка процесса с пределом 1,5 секунды. Не запускайте бинарник из произвольной искомой workspace.
- `quickPick.ts`: debounce 350 мс, generation для устаревших ответов, сериализация запросов, alwaysShow, late-response guard после закрытия и проверка актуальности превью перед открытием документа.
- `resultsView.ts`: TreeDataProvider, полный список последнего поиска, исходные диапазоны строк 1-based преобразуются в VS Code 0-based. Описание TreeView содержит запрос.
- `statusBar.ts`: ready/progress/error; ошибка открывает настройки. Output: Semantic Navigator.
- `engine/cmd/engine/main.go`: stdio маршрутизация, initialize, index queue, сканирование, кеш эмбеддингов чанков, поиск. Один индексатор; запрос во время прохода объединяется в следующий. Поисковые RPC читаются последовательно; provider timeout ограничивает блокировку.
- `engine/internal/storage/sqlite.go`: files/chunks/chunks_fts/metadata, ReplaceFile атомарно сохраняет чанки+FTS+хеш. EnsureFingerprint атомарно сбрасывает несовместимые данные, ListFiles+DeleteFile убирают отсутствующие пути. В SQLite один connection.
- `provider/openai.go`: `/embeddings`, batch 64, HTTP timeout 45 секунд, outer context search=20 / file=60 секунд. Ключ необязателен; не скрывайте auth/quota ошибки. Проверяются индексы ответа, количество, ненулевые/конечные векторы.
- `chunker/chunker.go`: эвристики, не AST. C# дополнительно маскирует строки/комментарии. Не обещайте полную поддержку C#; проверенные конструкции представлены тестами. Fallback: 80 строк, overlap 15.
- `search/hybrid.go`: косинус + RRF с k=60, weights=1/0.6 и штрафным BM25-рангом для отсутствующих совпадений. Полный перебор векторов; порога релевантности нет.

## Протокол

Одна JSON-строка на stdout; любые логи исключительно stderr. Объект запроса: `{"jsonrpc":"2.0","id":1,"method":"status","params":{}}`.

- `initialize`: workspacePath, storagePath, provider {type, baseUrl, apiKey, model}; возвращает engineVersion, supportedLanguages, storagePath. Повторный initialize запрещён: перезапустите процесс. URL/модель/workspace/версия чанкинга входят в fingerprint, секреты — нет.
- `index/start`: {forceReindex:boolean}; отвечает started или queued немедленно. Force очищает текущий индекс; без force переиспользуются хеши и векторы неизменившихся чанков.
- `index/progress`: scanning/chunking/embedding/done/error с totalFiles, processedFiles, totalChunks, percentage, currentFile, message. Наблюдать done/error необходимо отдельно от ответа index/start.
- `search`: {query, topK}; result {query, durationMs, matches:[]}. Match: filePath, relativePath, startLine, endLine, snippet, symbolName, score, matchReason. Пустой индекс не вызывает provider.
- `status`: state=idle/indexing/ready/error, indexedFiles, indexedChunks, lastError.
- `shutdown`: завершает процесс. Расширение обычно останавливает дочерний процесс сигналом, чтобы не ждать RPC за заблокированным поиском.

## Проверки и материалы

`engine/cmd/engine/main_test.go`: lifecycle, reuse, queued change, failure recovery, empty search, invalidation. `extension/test/ux.test.cjs`: mock VS Code и таймеров, пять целевых регрессий. `extension/test/host.cjs`: настоящий Extension Host + встроенный движок + loopback HTTP mock; запуск через `scripts/test-host.sh` с временным профилем, можно передать VSIX. Возвращаемый activate read-only API getStatus/search используется smoke-тестом.

`demo/CodeBY.Shop`: собственный MIT C#-проект без NuGet-пакетов. `evaluation/queries.json`: 30 ручных меток по относительному пути и символу. `scripts/evaluate.py`: изолированная SQLite на каждый прогон, timeout, метрики/latency, результаты по каждому запросу и хеши исходников/датасета. Ключ только из SEMANTIC_API_KEY, в отчёт не пишется.

Точные команды — README.md. `evaluation/mock-results.json` — функциональный mock, не доказательство RU→EN качества. Не отправляйте код реальному провайдеру без согласованного подключения. Не публикуйте релиз и не пушьте без отдельного запроса.
