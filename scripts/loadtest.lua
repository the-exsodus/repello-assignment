-- Generates a stream of LIMIT buy/sell orders around a central price so a
-- meaningful fraction cross and match, exercising the full matching path
-- rather than only the book-insertion path.
--
-- Symbols are spread across a small fixed set rather than hardcoded to one
-- ticker. This matters a lot for this engine's design: each symbol is
-- handled by its own single-goroutine actor, so a single-symbol test
-- serializes all requests onto one core no matter how many wrk threads or
-- connections you throw at it. Testing across several symbols is what
-- actually exercises the engine's cross-core parallelism.
local symbols = {"AAPL", "GOOGL", "MSFT", "TSLA", "AMZN"}

math.randomseed(os.time())

request = function()
  local side = "BUY"
  if math.random() > 0.5 then side = "SELL" end

  local symbol = symbols[math.random(#symbols)]
  local price = 10000 + math.random(-50, 50)
  local qty = math.random(1, 50)

  local body = string.format(
    '{"symbol":"%s","side":"%s","type":"LIMIT","price":%d,"quantity":%d}',
    symbol, side, price, qty
  )
  return wrk.format("POST", "/api/v1/orders", {["Content-Type"] = "application/json"}, body)
end
