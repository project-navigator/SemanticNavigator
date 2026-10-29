import unittest
from evaluate import metrics, percentile

class MetricsTests(unittest.TestCase):
    def test_known_ranks_and_multiple_labels(self):
        rows = [{'relevant':['a','b'], 'ranked':['x','a','y','z','q','b']},
                {'relevant':['c'], 'ranked':['x','y','z','q','v','c']},
                {'relevant':['d'], 'ranked':[]}]
        result = metrics(rows)
        self.assertAlmostEqual(result['Hit@5'], 1/3)
        self.assertAlmostEqual(result['Recall@5'], 1/6)
        self.assertAlmostEqual(result['MRR@10'], (1/2 + 1/6)/3)
        self.assertEqual(percentile([3,1,2,4,5], .95), 5)

    def test_unlabelled_or_empty_dataset_is_rejected(self):
        for rows in ([], [{'relevant':[], 'ranked':[]}]):
            with self.assertRaises(ValueError): metrics(rows)

if __name__ == '__main__': unittest.main()
