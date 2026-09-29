#!/usr/bin/env ruby
# Regenerate the compact conformance fixture from the verified upstream snapshot.
require 'digest'
require 'json'
require 'yaml'

expected = '82d802a1a2c938d73804e5bc73cdeb18265dca190ee819f6bd59919b570d1c5c'
raw = File.binread(ARGV.fetch(0), 1_000_001)
abort 'Pinned OpenAPI hash differs' unless Digest::SHA256.hexdigest(raw) == expected
names = %w[GetQuoteRequest GetQuoteResponse PostOrderRequest PostOrderResponse GetOrderResponse GetAssetsResponse]
schemas = YAML.safe_load(raw).fetch('components').fetch('schemas').select { |key, _| names.include?(key) }
def compact(value)
  case value
  when Hash
    value.reject { |key, _| %w[description example examples].include?(key) }.transform_values { |item| compact(item) }
  when Array
    value.map { |item| compact(item) }
  else
    value
  end
end
root = File.expand_path('..', __dir__)
File.write(File.join(root, 'internal/oif/testdata/schemas.json'), JSON.pretty_generate(compact(schemas)) + "\n")
