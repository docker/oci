// Copyright 2023 CUE Labs AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ociclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
)

// Repositories returns an iterator over repositories in the registry.
func (c *Client) Repositories(ctx context.Context, startAfter string) iter.Seq2[string, error] {
	return pager(ctx, c, pageRequest{
		URL:   catalogURL(startAfter),
		Scope: catalogScope(),
		Limit: -1,
		Next: func(last any) string {
			return catalogURL(fmt.Sprint(last))
		},
	}, true, func(resp *http.Response) ([]string, error) {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var catalog struct {
			Repos []string `json:"repositories"`
		}
		if err := json.Unmarshal(data, &catalog); err != nil {
			return nil, fmt.Errorf("cannot unmarshal catalog response: %v", err)
		}
		return catalog.Repos, nil
	})
}

func catalogURL(last string) string {
	return "/v2/_catalog" + listQuery(-1, last)
}
