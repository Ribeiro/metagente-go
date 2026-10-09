package elt

import "text/template"

// The agents that a description makes. They are the Extractor and the Worker of samples/async-elt, with
// the table, the columns, the key and the sizes taken from the description, and the part that the model
// step has left out: a job that needs it is written by hand, as the sample is.

var extractorText = template.Must(template.New("extractor").Parse(`agent Extractor
  goal "Copy the table {{.Table}} into batch events on the broker, page by page, and be able to start again where it stopped"
  tool source from sql "{{.Source}}"
  tool outbox from sql "{{.Outbox}}"
  tool events from broker "{{.Broker}}" publish "etl.*.batch" "etl.*.control"
  tool codec
  tool clock

  # job is the name of the job: a new one starts it, the same one starts it again where it stopped.
  accepts extract job  # copy the table, and say how many batches and rows went out
  # What the destination asks for when a batch never arrived (the sweeper of the destination asks).
  accepts resend v job_id seq  # build a batch again from the edges in the outbox, and publish it again
  on extract
    version = outbox.version
    if version is not 1
      fail "the outbox has version {version}, and this Extractor works with version 1"
    page_size = outbox.int n: {{.Rows}}
    limit = outbox.int n: {{.Bytes}}
    more = yes
    repeat while more
      todo = outbox.next_planned job: job
      if todo is nothing
        # Nothing waits: plan the next batch. A page that is too big is halved until it fits.
        seq = outbox.next_seq job: job
        after = outbox.last_upto job: job
        n = page_size
        fits = no
        repeat while not fits up to 40 times
          rows = source.page after: after size: n
          weight = codec.size value: rows
          if weight is more than limit and n is more than 1
            n = outbox.half n: n
          otherwise
            fits = yes
        upto = nothing
        for row in rows
          upto = row.{{.Key}}
        if upto is nothing
          more = no
        otherwise
          planned = codec.count value: rows
          saved = outbox.plan job: job seq: seq after_key: after upto_key: upto row_count: planned
        batch_seq = seq
        batch_after = after
        batch_rows = rows
      otherwise
        # A batch was planned and never confirmed: build it again from the same edges.
        batch_seq = todo.seq
        batch_after = todo.after_key
        upto = todo.upto_key
        batch_rows = source.range after: batch_after upto: upto
      if upto is not nothing
        count = codec.count value: batch_rows
        table = codec.table rows: batch_rows columns: {{.Columns}}
        body = codec.json value: table
        hash = codec.sha256 text: body
        payload = codec.gzip text: body
        event_id = codec.uuid
        read_at = clock.now
        event = codec.record v: 1 event_id: event_id job_id: job seq: batch_seq table: "{{.Table}}" key: "{{.Key}}" after: batch_after upto: upto read_at: read_at.text columns: {{.Columns}} row_count: count encoding: "json+gzip" payload: payload sha256: hash
        events.publish subject: "etl.{job}.batch" id: "{job}:{batch_seq}" data: event
        confirmed = outbox.confirm job: job seq: batch_seq
    totals = outbox.totals job: job
    control = codec.record v: 1 job_id: job kind: "planned" batches: totals.batches rows: totals.rows
    events.publish subject: "etl.{job}.control" id: "{job}:control" data: control
    reply "job {job}: {totals.batches} batches, {totals.rows} rows"

  on resend
    edges = outbox.edges job: job_id seq: seq
    if edges is nothing
      reply "batch {seq} of {job_id} is not in this outbox"
    batch_rows = source.range after: edges.after_key upto: edges.upto_key
    count = codec.count value: batch_rows
    table = codec.table rows: batch_rows columns: {{.Columns}}
    body = codec.json value: table
    hash = codec.sha256 text: body
    payload = codec.gzip text: body
    event_id = codec.uuid
    read_at = clock.now
    event = codec.record v: 1 event_id: event_id job_id: job_id seq: seq table: "{{.Table}}" key: "{{.Key}}" after: edges.after_key upto: edges.upto_key read_at: read_at.text columns: {{.Columns}} row_count: count encoding: "json+gzip" payload: payload sha256: hash
    events.publish subject: "etl.{job_id}.batch" id: "{job_id}:{seq}" data: event
    reply "batch {seq} of {job_id} sent again with {count} rows"
`))

var workerText = template.Must(template.New("worker").Parse(`agent Worker
  goal "Land the batches of an ETL job in the destination, transform them, and close the job"
  tool dest from sql "{{.Dest}}"
  tool codec

  # One batch, as the Extractor publishes it. The fields are all of the event.
  accepts batch v event_id job_id seq table key after upto read_at columns row_count encoding payload sha256  # land one batch and transform it
  # The end of a job at the source: how many batches and rows to expect.
  accepts control v job_id kind batches rows  # register the totals of a job, and close it if everything is done
  # Staging holds the rows as they came: clean it from time to time.
  accepts purge days  # delete from staging the batches that were done more than this many days ago
  # The control tables are cleaned too, much later than staging: only the jobs that are done and have nothing in staging.
  accepts purge_control days  # delete from the control tables the jobs that were done more than this many days ago
  accepts resume job  # let a paused job go on
  # What the sweeper asks for: transform again a batch that stayed landed or failed, from the rows in staging.
  accepts retransform v job_id seq  # transform again a batch whose rows are in staging

  on batch
    version = dest.version
    if version is not 1
      fail "the destination has version {version} of the control tables, and this Worker works with version 1"
    if v is not 1
      fail "the event has version {v}, and this Worker reads version 1"
    if encoding is not "json+gzip"
      fail "the event is encoded as {encoding}, and this Worker reads json+gzip"
    if columns is not {{.Columns}}
      fail "the columns of the event are not the ones this Worker lands"
    # A paused job takes no batches: the event is asked for again later, so it waits for the person to resume it.
    status = dest.job_status job: job_id
    if status is not nothing and status.state is "paused"
      fail "job {job_id} is paused ({status.reason}): resume it when the cause is solved" retry in 3600 seconds
    state = dest.batch_state job: job_id seq: seq
    if state is "done"
      reply "batch {seq} of {job_id} was already done"
    if state is nothing
      # Not landed yet: check the event, and land it (transaction 1).
      body = codec.gunzip text: payload
      check = codec.sha256 text: body
      if check is not sha256
        noted = dest.record_incident job: job_id seq: seq code: "HASH_MISMATCH"
        fail "the hash of batch {seq} of {job_id} does not match: the event is damaged"
      rows = codec.parse text: body
      landed = dest.land_batch job: job_id seq: seq row_count: row_count rows: rows
    # Landed (now, or by a copy of this event before): the brake, and then transaction 2.
    share = dest.reject_share job: job_id seq: seq
    if share is more than {{.RejectShare}}
      marked = dest.fail_batch job: job_id seq: seq code: "TOO_MANY_REJECTS"
      stopped = dest.pause_if_failing job: job_id
      fail "more than {{.RejectShare}} percent of batch {seq} of {job_id} would be rejected: it is probably a change in the source"
    transformed = dest.transform_batch job: job_id seq: seq version: "v1"
    closed = dest.try_close job: job_id
    counts = dest.batch_counts job: job_id seq: seq
    reply "batch {seq} of {job_id}: {counts.rows_loaded} loaded, {counts.rows_rejected} rejected"

  on control
    registered = dest.register_totals job: job_id batches: batches rows: rows
    closed = dest.try_close job: job_id
    state = dest.job_state job: job_id
    reply "job {job_id} is {state}"

  on purge
    removed = dest.purge days: days
    reply "removed {removed} rows from staging"

  on purge_control
    removed = dest.purge_control days: days
    reply "removed {removed.old_jobs} finished jobs from the control tables"

  on resume
    resumed = dest.resume job: job
    reply "job {job}: {resumed.resume_job} paused job is running again"

  on retransform
    version = dest.version
    if version is not 1
      fail "the destination has version {version} of the control tables, and this Worker works with version 1"
    status = dest.job_status job: job_id
    if status is not nothing and status.state is "paused"
      fail "job {job_id} is paused ({status.reason}): resume it when the cause is solved" retry in 3600 seconds
    state = dest.batch_state job: job_id seq: seq
    if state is nothing
      reply "batch {seq} of {job_id} has no rows here: it has to be sent again by the Extractor"
    if state is "done"
      reply "batch {seq} of {job_id} was already done"
    share = dest.reject_share job: job_id seq: seq
    if share is more than {{.RejectShare}}
      marked = dest.fail_batch job: job_id seq: seq code: "TOO_MANY_REJECTS"
      stopped = dest.pause_if_failing job: job_id
      fail "more than {{.RejectShare}} percent of batch {seq} of {job_id} would be rejected: it is probably a change in the source"
    transformed = dest.transform_batch job: job_id seq: seq version: "v1"
    closed = dest.try_close job: job_id
    counts = dest.batch_counts job: job_id seq: seq
    reply "batch {seq} of {job_id} transformed again: {counts.rows_loaded} loaded, {counts.rows_rejected} rejected"
`))
